package main

import (
	"fmt"
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
				Description: "List of Slack users to invite, the following formats are supported: 'email:user@some.domain', 'bot:botname', 'id:userid'",
				Required:    true,
				MinItems:    1,
				// TODO: validate that the ":" separator is present
				// ValidateFunc: validation.StringInSlice(),
			},
			"members_ids": &schema.Schema{
				Type:     schema.TypeList,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Computed: true,
			},
		},
	}
}

// Returnes (*slack.User, error) from an email
func getUserByEmail(api *slack.Client, email string) (*slack.User, error) {
	user, err := api.GetUserByEmail(email)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// Returns (*slack.User, error) from a user expression (i.e. "bot:mybot", "id:myid", "email:my@email.corp")
func getUserInfo(api *slack.Client, userExpression string) (*slack.User, error) {
	userIdentifier := strings.SplitAfter(userExpression, ":")[1]

	if strings.Contains(userExpression, "email:") {
		return getUserByEmail(api, userIdentifier)
	}
	if strings.Contains(userExpression, "id:") {
		return api.GetUserInfo(userIdentifier)
	}
	if strings.Contains(userExpression, "bot:") {
		users, err := api.GetUsers()
		if err != nil {
			return nil, fmt.Errorf("getUserInfo: Could not get the list of users")
		}
		for i, u := range users {
			if !u.IsBot {
				continue
			}
			if u.Name == userIdentifier {
				return api.GetUserInfo(u.ID)
			}
			if i == len(users)-1 {
				return nil, fmt.Errorf("Could not find the bot: %s", userIdentifier)
			}
		}
	}
	return nil, fmt.Errorf("only 'bot:*' , 'id:*' and 'email:*' member expressions are supported: %s", userExpression)
}

// Returns a list of memberIDs to kick when given a list of managed slack users that, them, should be present
// Authoritative for a given channel
func getConversationUserIdsToKickAuthoritative(api *slack.Client, c *slack.Channel, managedUsers []*slack.User) ([]string, error) {
	var userIdsToKick []string

	conversationMembers, _, err := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
		ChannelID: c.ID,
		Cursor:    "",
		Limit:     0,
	})
	if err != nil {
		return nil, fmt.Errorf("getConversationUserIdsToKickAuthoritative: Could not get the list of users in the conversation %s! %s", c.Name, err)
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

// Returns a list of memberIDs to invite when given a list of managed slack users that should be present
func getConversationUserIdsToInvite(api *slack.Client, c *slack.Channel, managedUsers []*slack.User) ([]string, error) {
	var usersIdsToInvite []string

	conversationMembers, _, err := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
		ChannelID: c.ID,
		Cursor:    "",
		Limit:     0,
	})
	if err != nil {
		return nil, fmt.Errorf("getConversationUserIdsToInvite: Could not get the list of users in the conversation %s! %s", c.Name, err)
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
	// TODO: move the sort to the create/update resources
	sort.Strings(usersIdsToInvite)
	return usersIdsToInvite, nil
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
		Cursor:    "",
		Limit:     0,
	})
	if err != nil {
		return fmt.Errorf("resourceConversationMembersRead: Could not get the list of users in the conversation %s! %s", c.Name, err)
	}

	sort.Strings(conversationMembers)

	// TODO: set partial state?
	err = d.Set("conversation_id", c.ID)
	if err != nil {
		return fmt.Errorf("Could not set conversation_id within terraform state: %s", err)
	}

	err = d.Set("members_ids", conversationMembers)
	if err != nil {
		return fmt.Errorf("Could not set members_ids within terraform state: %s", err)
	}

	return nil
}

func resourceConversationMembersCreate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	c, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return fmt.Errorf("Could not get conversation details: %s", err)
	}

	members := d.Get("members").([]interface{})
	managedUsers := make([]*slack.User, len(members))
	for i, m := range members {
		managedUsers[i], err = getUserInfo(api, m.(string))
		if err != nil {
			return err
		}
	}

	usersToInvite, err := getConversationUserIdsToInvite(api, c, managedUsers)
	if err != nil {
		return err
	}
	// Invite all relevant users in a single API call
	_, err = api.InviteUsersToConversation(c.ID, usersToInvite...)
	if err != nil {
		return fmt.Errorf("Could not invite users to conversation: %s", err)
	}

	d.SetId(c.ID)
	return resourceConversationMembersRead(d, meta)
}

func resourceConversationMembersUpdate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	c, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return fmt.Errorf("Could not get conversation information: %s", err)
	}

	members := d.Get("members").([]interface{})
	managedUsers := make([]*slack.User, len(members))
	for i, m := range members {
		managedUsers[i], err = getUserInfo(api, m.(string))
		if err != nil {
			return err
		}
	}

	userIdsToInvite, err := getConversationUserIdsToInvite(api, c, managedUsers)
	if err != nil {
		return fmt.Errorf("Could not get members'ids to invite: %s", err)
	}

	// Invite new users
	c, err = api.InviteUsersToConversation(c.ID, userIdsToInvite...)
	if err != nil {
		return fmt.Errorf("Could not invite users")
	}

	// Kick previously managed users only
	// (non-authoritative for a given conversation)
	oldMembers, newMembers := d.GetChange("members")
	for _, o := range oldMembers.([]string) {
		for i, n := range newMembers.([]string) {
			if o == n {
				continue
			}
			if i == len(newMembers.([]string))-1 {
				u, err := getUserInfo(api, o)
				if err != nil {
					return fmt.Errorf("Could not get old user %s information: %s", o, err)
				}
				err = api.KickUserFromConversation(c.ID, u.ID)
				if err != nil {
					return fmt.Errorf("Could not kick user %s from conversation: %s", o, err)
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
		if err != nil && err.Error() != "user_not_found" && err.Error() != "conversation_not_found" && err.Error() == "not_in_conversation" {
			return fmt.Errorf("Could not uninvite user %s from conversation: %s", m, err)
		}
	}
	return nil
}
