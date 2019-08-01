package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/nlopes/slack"
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

func getUserByEmail(api *slack.Client, email string) (*slack.User, error) {
	user, err := api.GetUserByEmail(email)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func getUserInfo(api *slack.Client, userExpression string) (*slack.User, error) {
	userIdentifier := strings.SplitAfter(userExpression, ":")[1]

	if strings.Contains(userExpression, "email:") {
		return getUserByEmail(api, userIdentifier)
	}
	if strings.Contains(userExpression, "id:") {
		return api.GetUserInfo(userIdentifier)
	}
	if strings.Contains(userExpression, "bot:") {
		if users, err := api.GetUsers(); err != nil {
			for _, u := range users {
				if !u.IsBot {
					continue
				}
				if !(u.Name == userIdentifier) {
					continue
				}
				return api.GetUserInfo(u.ID)
			}
		} else {
			return nil, err
		}
	}
	return nil, errors.New("only 'bot:*' , 'id:*' and 'email:*' member expressions are supported")
}

func conversationMembersIDToInvite(api *slack.Client, c *slack.Channel, userExpressions []string) ([]string, error) {
	var usersToInvite []string
	usersManaged := make([]string, 0, len(userExpressions))

	for _, uE := range userExpressions {
		user, err := getUserInfo(api, uE)
		if err != nil {
			return nil, fmt.Errorf("getUserInfo: %s", err)
		}
		s := usersManaged[:0]
		// return nil, fmt.Errorf("uid: %s", user.ID)
		s = append(s, user.ID)
	}

	return nil, fmt.Errorf("slice: %s", usersManaged)

	conversationMembers, _, _ := api.GetUsersInConversation(&slack.GetUsersInConversationParameters{
		ChannelID: c.ID,
		Cursor:    "",
		Limit:     0,
	})

	for i, um := range usersManaged {
		for _, cm := range conversationMembers {
			if um == cm {
				continue
			}
			if i == len(usersManaged)-1 {
				usersToInvite = append(usersToInvite, um)
			}
		}
	}

	sort.Strings(usersToInvite)
	return usersToInvite, nil
}

func resourceConversationMembersRead(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)

	conversation, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		d.SetId("")
		return fmt.Errorf("Could not get conversation details: %s", err)
	}

	if err := d.Set("conversation_id", conversation.ID); err != nil {
		return fmt.Errorf("Could not set conversation_id within terraform state: %s", err)
	}

	membersIds := conversation.Members
	sort.Strings(membersIds)
	if err := d.Set("members_ids", membersIds); err != nil {
		return fmt.Errorf("Could not set members_ids within terraform state: %s", err)
	}

	return nil
}

func resourceConversationMembersCreate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	conversation, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return fmt.Errorf("Could not get conversation details: %s", err)
	}

	members := d.Get("members").([]interface{})
	m := make([]string, len(members))
	for i, v := range members {
		m[i] = v.(string)
	}

	usersToInvite, err := conversationMembersIDToInvite(api, conversation, m)
	if err != nil {
		return fmt.Errorf("could not convert members attribute to []string: %s", err)
	}
	// Try to invite all users in one API call
	if _, err := api.InviteUsersToConversation(conversation.ID, usersToInvite...); err != nil {
		for _, u := range usersToInvite {
			// Try one by one if that does not work
			if _, err := api.InviteUsersToConversation(conversation.ID, u); err != nil {
				return fmt.Errorf("Could not invite user %s to conversation: %s", u, err)
			}
		}
	}

	d.SetId(conversation.ID)
	return resourceConversationMembersRead(d, meta)
}

func resourceConversationMembersUpdate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)

	conversation, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return fmt.Errorf("Could not get conversation information: %s", err)
	}

	members := d.Get("members").([]interface{})
	m := make([]string, len(members))
	for i, v := range members {
		m[i] = v.(string)
	}

	if d.HasChange("members") || d.HasChange("members_ids") {
		usersToInvite, err := conversationMembersIDToInvite(api, conversation, m)
		if err != nil {
			return fmt.Errorf("Could not get members ids to from members to invite: %s", err)
		}

		// Invite new users
		if _, err := api.InviteUsersToConversation(conversation.ID, usersToInvite...); err != nil {
			for _, u := range usersToInvite {
				// Try one by one if that does not work
				if _, err := api.InviteUsersToConversation(conversation.ID, u); err != nil {
					return fmt.Errorf("Could not invite user %s to conversation: %s", u, err)
				}
			}
		}

		// Remove absent users
		oldMembersEmails, newMembersEmails := d.GetChange("members")
		for _, o := range oldMembersEmails.([]string) {
			for _, n := range newMembersEmails.([]string) {
				if o == n {
					continue
				}
				oldUser, err := getUserInfo(api, o)
				if err != nil {
					return fmt.Errorf("Could not get old user %s information: %s", o, err)
				}
				if err := api.KickUserFromConversation(conversation.ID, oldUser.ID); err != nil {
					return fmt.Errorf("Could not uninvite user %s from conversation: %s", o, err)
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
		switch err := api.KickUserFromConversation(d.Get("conversation_id").(string), mi); err.Error() {
		case "user_not_found":
			continue
		case "conversation_not_found":
			continue
		case "not_in_conversation":
			continue
		default:
			return fmt.Errorf("Could not uninvite user %s from conversation: %s", m, err)
		}
	}
	return nil
}
