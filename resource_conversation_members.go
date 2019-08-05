package main

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/timdurward/slack"
)

func resourceConversationMembers() *schema.Resource {
	return &schema.Resource{
		Create: resourceConversationMembersCreate,
		Read:   resourceConversationMembersRead,
		Update: resourceConversationMembersUpdate,
		Delete: resourceConversationMembersDelete,

		Schema: map[string]*schema.Schema{
			"conversation_id": &schema.Schema{
				Type:        schema.TypeString,
				Description: "The conversationID of the Slack conversation, this resource is authoritative for a given conversation ID",
				Required:    true,
			},
			"members": &schema.Schema{
				Type:        schema.TypeList,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "List of Slack users to invite, the following formats are supported: 'email:user@some.domain', 'id:userid'",
				Required:    true,
				MinItems:    1,
				// TODO: validate that the ":" separator is present, once ValidateFunc is supported on lists
				// ValidateFunc: validation.StringInSlice([]string{"foo:"}, false),
			},
			"members_ids": &schema.Schema{
				Type:     schema.TypeList,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Computed: true,
			},
			"authoritative": &schema.Schema{
				Type:        schema.TypeBool,
				Optional:    true,
				Required:    false,
				Default:     false,
				ForceNew:    true,
				Description: "if set to true, any member not present within the members attributes will be forcibly kicked out from the conversation (except for the token owner) (default is false)",
			},
		},
	}
}

// Returns (*slack.User, error) from an email
func getUserByEmail(api *slack.Client, email string) (*slack.User, error) {
	user, err := api.GetUserByEmail(email)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// Returns (*slack.User, error) from a user expression (i.e. "id:myId", "email:my@email.corp")
func getUserInfo(api *slack.Client, userExpression string) (*slack.User, error) {
	userIdentifier := strings.SplitAfter(userExpression, ":")[1]
	switch {
	case strings.Contains(userExpression, "email:"):
		return getUserByEmail(api, userIdentifier)
	case strings.Contains(userExpression, "id:"):
		return api.GetUserInfo(userIdentifier)
	}
	return nil, fmt.Errorf("only 'id:*' and 'email:*' member expressions are supported: %s", userExpression)
}

// Kicks users out of a given conversation
func kickUsers(api *slack.Client, c *slack.Channel, users []*slack.User) error {
	for _, u := range users {
		err := api.KickUserFromConversation(c.ID, u.ID)
		if err != nil {
			switch err.Error() {
			case "cant_kick_self":
				if _, err = api.LeaveConversation(c.ID); err != nil {
					return fmt.Errorf("could not self-leave the conversation: %s", err)
				}
			case "not_in_channel":
				continue
			case "user_not_found":
				continue
			case "channel_not_found":
				continue
			//TODO: Should actually break or be handled better
			//case "method_not_supported_for_channel_type":
			//	continue
			//TODO: Should check that general is not the managed conversation way before
			case "cant_kick_from_general":
				continue
			default:
				return fmt.Errorf("could not kick user %s out of conversation: %s", u.Name, err)
			}
		}
	}
	return nil
}

// Invite users within a given conversation
func inviteUsers(api *slack.Client, c *slack.Channel, managedUsers []*slack.User) error {
	//var usersIdsToInvite []string
	//conversationMembers, _, err := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
	//	ChannelID: c.ID,
	//	Cursor:    "", // TODO: implement a cursor for paginated API reads
	//	Limit:     0,
	//})
	//if err != nil {
	//	return fmt.Errorf("could not get the list of users in the conversation %s! %s", c.Name, err)
	//}
	// Reduces the number of API calls by figuring out which users are already invited
	//for _, mu := range managedUsers {
	//	for i, cm := range conversationMembers {
	//		if mu.ID == cm {
	//			continue
	//		}
	//		if i == len(conversationMembers)-1 {
	//			usersIdsToInvite = append(usersIdsToInvite, mu.ID)
	//		}
	//	}
	//}
	// Invite all relevant users in a single API call
	//_, err = api.InviteUsersToConversation(c.ID, usersIdsToInvite...)
	//if err != nil {
	// Retry one by one to pinpoint the problematic userID
	for _, u := range managedUsers {
		_, err := api.InviteUsersToConversation(c.ID, u.ID)
		if err != nil {
			switch {
			case err.Error() == "cant_invite_self":
				if _, _, _, err = api.JoinConversation(c.ID); err != nil {
					return fmt.Errorf("could not self-join conversation: %s", err)
				}
				continue
			case err.Error() == "already_in_channel":
				continue
			default:
				return fmt.Errorf("could not invite userID %s to conversation: %s", u.ID, err)
			}
		}
	}
	//}
	return nil
}

func resourceConversationMembersRead(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	c, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		d.SetId("")
		return nil
	}

	conversationMembers, _, err := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
		ChannelID: c.ID,
		Cursor:    "", // TODO: implement a cursor for paginated API reads
		Limit:     0,
	})
	if err != nil {
		return fmt.Errorf("resourceConversationMembersRead: could not get the list of users in the conversation %s! %s", c.Name, err)
	}
	// Sort the members' list before persisting it within terraform state, to avoid un-needed state changes
	sort.Strings(conversationMembers)

	// Synchronize terraform state's members attribute relative to present conversation members
	members := d.Get("members").([]interface{})
	presentMembers := make([]string, 0)
	presentMembersIds := make([]string, 0)

	for _, m := range members {
		mi, _ := getUserInfo(api, m.(string))
		for _, cm := range conversationMembers {
			if mi.ID == cm {
				presentMembers = append(presentMembers, m.(string))
				presentMembersIds = append(presentMembersIds, mi.ID)
				break
			}
		}
	}

	if err = d.Set("members", presentMembers); err != nil {
		return err
	}
	if err = d.Set("members_ids", presentMembersIds); err != nil {
		return err
	}
	return nil
}

func resourceConversationMembersCreate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	c, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return fmt.Errorf("could not get conversation details: %s", err)
	}

	members := d.Get("members").([]interface{})
	managedUsers := make([]*slack.User, len(members))
	for i, m := range members {
		managedUsers[i], err = getUserInfo(api, m.(string))
		if err != nil {
			return err
		}
	}
	err = inviteUsers(api, c, managedUsers)
	if err != nil {
		return err
	}
	b := strings.Builder{}
	b.WriteString(c.ID)
	b.WriteString("-members")
	d.SetId(b.String())
	return resourceConversationMembersRead(d, meta)
}

func resourceConversationMembersUpdate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	c, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return fmt.Errorf("could not get conversation information: %s", err)
	}

	members := d.Get("members").([]interface{})
	managedUsers := make([]*slack.User, len(members))
	for i, m := range members {
		managedUsers[i], err = getUserInfo(api, m.(string))
		if err != nil {
			return err
		}
	}
	err = inviteUsers(api, c, managedUsers)
	if err != nil {
		return err
	}

	usersToKick := make([]*slack.User, 0)
	if !d.Get("authoritative").(bool) {
		// Kick previously managed users ONLY
		// (non-authoritative for a given conversation)
		oldMembers, newMembers := d.GetChange("members")
		for _, o := range oldMembers.([]interface{}) {
			for i, n := range newMembers.([]interface{}) {
				if reflect.DeepEqual(o, n) {
					break
				}
				if i == len(newMembers.([]interface{}))-1 {
					u, err := getUserInfo(api, o.(string))
					if err != nil {
						return fmt.Errorf("could not get old user %s information: %s", o.(string), err)
					}
					usersToKick = append(usersToKick, u)
				}
			}
		}
	} else {
		// Kick all users not managed by terraform
		// (authoritative for a given conversation)
		conversationMembers, _, err := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
			ChannelID: c.ID,
			Cursor:    "", // TODO: implement a cursor for paginated API reads
			Limit:     0,
		})
		if err != nil {
			return fmt.Errorf("(kickUsers) could not get the list of users in the conversation %s! %s", c.Name, err)
		}
		for _, m := range conversationMembers {
			for i, u := range managedUsers {
				if m == u.ID {
					break
				}
				if i == len(managedUsers)-1 {
					usersToKick = append(usersToKick, u)
				}
			}
		}
	}
	if len(usersToKick) > 0 {
		err = kickUsers(api, c, usersToKick)
		if err != nil {
			return err
		}
	}
	return resourceConversationMembersRead(d, meta)
}

func resourceConversationMembersDelete(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	usersToKick := make([]*slack.User, 0)

	c, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return fmt.Errorf("could not get conversation information: %s", err)
	}

	// Kick all users in case of simultaneous state change + resource destruction
	members := make(map[string]string, 0)
	membersKeys := make([]string, 0)
	
	old, new := d.GetChange("members")
	for _, o := range old.([]interface{}) {
		members[o.(string)] = ""
		membersKeys = append(membersKeys, o.(string))
	}
	for _, n := range new.([]interface{}) {
		if _, ok := members[n.(string)]; !ok {
			members[n.(string)] = ""
			membersKeys = append(membersKeys, n.(string))
		}
	}
	
	for _, m := range membersKeys {
		u, err := getUserInfo(api, m)
		if err != nil {
			switch err.Error() {
			case "user_not_found":
				continue
			default:
				return err
			}
		}
		usersToKick = append(usersToKick, u)
	}

	return kickUsers(api, c, usersToKick)
}
