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
				Description: "The conversationID of the Slack conversation, this resource is authoritative for a given conversation ID, do not create another slack_conversation_members resource pointing to the same conversationID, or they will fight each other",
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
				ForceNew: true,
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

// Returns a list of memberIDs to kick when given a list of managed slack users that, them, should be present
// Authoritative for a given channel
func getConversationUserIdsToKickAuthoritative(api *slack.Client, c *slack.Channel, managedUsers []*slack.User) ([]string, error) {
	var userIdsToKick []string

	conversationMembers, _, err := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
		ChannelID: c.ID,
		Cursor:    "", // TODO: implement a cursor for paginated API reads
		Limit:     0,
	})
	if err != nil {
		return nil, fmt.Errorf("getConversationUserIdsToKickAuthoritative: could not get the list of users in the conversation %s! %s", c.Name, err)
	}

	for _, cm := range conversationMembers {
		for i, mu := range managedUsers {
			if cm == mu.ID {
				continue
			}
			if i == len(managedUsers)-1 {
				userIdsToKick = append(userIdsToKick, mu.ID)
			}
		}
	}
	return userIdsToKick, nil
}

func inviteUsers(api *slack.Client, c *slack.Channel, managedUsers []*slack.User) error {
	var usersIdsToInvite []string

	conversationMembers, _, err := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
		ChannelID: c.ID,
		Cursor:    "", // TODO: implement a cursor for paginated API reads
		Limit:     0,
	})
	if err != nil {
		return fmt.Errorf("could not get the list of users in the conversation %s! %s", c.Name, err)
	}

	for _, mu := range managedUsers {
		for i, cm := range conversationMembers {
			if mu.ID == cm {
				continue
			}
			if i == len(conversationMembers)-1 {
				usersIdsToInvite = append(usersIdsToInvite, mu.ID)
			}
		}
	}
	// Invite all relevant users in a single API call
	_, err = api.InviteUsersToConversation(c.ID, usersIdsToInvite...)
	if err != nil {
		// Retry one by one to pinpoint the problematic userID
		for _, u := range usersIdsToInvite {
			_, err := api.InviteUsersToConversation(c.ID, u)
			if err != nil {
				switch {
				case err.Error() == "cant_invite_self":
					continue
				case err.Error() == "already_in_channel":
					continue
				default:
					return fmt.Errorf("could not invite userID %s to conversation: %s", u, err)
				}
			}
		}
	}
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

	err = d.Set("conversation_id", c.ID)
	if err != nil {
		return fmt.Errorf("could not set conversation_id within terraform state: %s", err)
	}

	// Synchronize terraform state's members attribute relative to present conversation members
	members := d.Get("members").([]interface{})
	presentMembers := make([]string, 0)

	for _, m := range members {
		mi, _ := getUserInfo(api, m.(string))
		for _, cm := range conversationMembers {
			cmj, _ := api.GetUserInfo(cm)
			if mi.ID != cmj.ID {
				continue
			}
			presentMembers = append(presentMembers, m.(string))
			break
		}
	}
	// Sort the members' list before persisting it within terraform state, to avoid un-needed state changes
	sort.Strings(conversationMembers)
	if err = d.Set("members", presentMembers); err != nil {
		return err
	}
	if err = d.Set("members_ids", conversationMembers); err != nil {
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
	d.SetId(c.ID)
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

	// Kick previously managed users only
	// (non-authoritative for a given conversation)
	oldMembers, newMembers := d.GetChange("members")
	for _, o := range oldMembers.([]interface{}) {
		for i, n := range newMembers.([]interface{}) {
			if reflect.DeepEqual(o, n) {
				continue
			}
			if i == len(newMembers.([]interface{}))-1 {
				u, err := getUserInfo(api, o.(string))
				if err != nil {
					return fmt.Errorf("could not get old user %s information: %s", o.(string), err)
				}
				err = api.KickUserFromConversation(c.ID, u.ID)
				if err != nil {
					switch err.Error() {
					case "cant_kick_self":
						continue
					case "not_in_channel":
						continue
					case "user_not_found":
						continue
					case "channel_not_found":
						continue
					//TODO: Should actually break or be handled better
					//case "method_not_supported_for_channel_type":
					//	continue
					case "cant_kick_from_general":
						continue
					default:
						return fmt.Errorf("could not kick user %s from conversation: %s", o.(string), err)
					}
				}
			}
		}
	}
	return resourceConversationMembersRead(d, meta)
}

func resourceConversationMembersDelete(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)

	members := d.Get("members").([]interface{})
	m := make([]string, len(members))
	for i, v := range members {
		m[i] = v.(string)
	}

	for _, mi := range m {
		u, err := getUserInfo(api, mi)
		if err != nil {
			return err
		}
		err = api.KickUserFromConversation(d.Get("conversation_id").(string), u.ID)
		if err != nil {
			switch err.Error() {
			case "cant_kick_self":
				continue
			case "not_in_channel":
				continue
			case "user_not_found":
				continue
			case "channel_not_found":
				continue
			case "method_not_supported_for_channel_type":
				continue
			case "cant_kick_from_general":
				continue
			default:
				return fmt.Errorf("could not kick user %s from conversation: %s", mi, err)
			}
		}
	}
	return nil
}
