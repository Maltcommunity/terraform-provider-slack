package main

import (
	"bytes"
	"errors"
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

	for _, uE := range userExpressions {
		user, err := getUserInfo(api, uE)
		if err != nil {
			return nil, err
		}
		for i, cm := range c.Members {
			if user.ID == cm {
				continue
			}
			if i == len(c.Members)-1 {
				usersToInvite = append(usersToInvite, user.ID)
			}
		}
	}
	sort.Strings(usersToInvite)
	return usersToInvite, nil
}

func resourceConversationMembersRead(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	var usersInvited []string
	// var usersToInvite []string

	if conversation, err := api.GetConversationInfo(d.Get("conversation_id").(string), false); err.Error() == "conversation_not_found" {
		// Remove the resource if the conversation is not present anymore
		d.SetId("")
	} else if err != nil {
		return err
	} else {
		if err := d.Set("conversation_id", conversation.ID); err != nil {
			return err
		}
		for _, m := range d.Get("members_ids").([]string) {
			for _, cm := range conversation.Members {
				if cm == m {
					usersInvited = append(usersInvited, cm)
				}
			}
		}
		sort.Strings(usersInvited)
		if err := d.Set("members_ids", usersInvited); err != nil {
			return err
		}
	}
	return nil
}

func resourceConversationMembersCreate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	var resourceID bytes.Buffer
	conversation, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return err
	}

	resourceID.WriteString("conversation-members-")
	resourceID.WriteString(conversation.ID)
	d.SetId(resourceID.String())

	if members, ok := d.Get("members").([]string); ok {
		usersToInvite, err := conversationMembersIDToInvite(api, conversation, members)
		if err != nil {
			return err
		}
		if _, err := api.InviteUsersToConversation(conversation.ID, usersToInvite...); err != nil {
			return err
		}
		sort.Strings(usersToInvite)
		err = d.Set("members_ids", usersToInvite)
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("could not convert members attribute to []string")
	}

	return resourceConversationMembersRead(d, meta)
}

func resourceConversationMembersUpdate(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)

	conversation, err := api.GetConversationInfo(d.Get("conversation_id").(string), false)
	if err != nil {
		return err
	}

	if members, ok := d.Get("members").([]string); ok && (d.HasChange("members") || d.HasChange("members_ids")) {
		usersToInvite, err := conversationMembersIDToInvite(api, conversation, members)
		if err != nil {
			return err
		}

		// Invite new users
		if _, err := api.InviteUsersToConversation(conversation.ID, usersToInvite...); err != nil {
			return err
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
					return err
				}
				api.KickUserFromConversation(conversation.ID, oldUser.ID)
			}
		}

		// Sync state
		var ids []string
		for _, e := range d.Get("members").([]string) {
			user, err := getUserInfo(api, e)
			if err != nil {
				return err
			}
			ids = append(ids, user.Profile.Email)
		}
		sort.Strings(ids)
		if err := d.Set("members_ids", ids); err != nil {
			return err
		}
	}
	return resourceConversationMembersRead(d, meta)
}

func resourceConversationMembersDelete(d *schema.ResourceData, meta interface{}) error {
	api := slack.New(meta.(*Config).APIToken)
	for _, m := range d.Get("members_ids").([]string) {
		switch err := api.KickUserFromConversation(d.Get("conversation_id").(string), m); err.Error() {
		case "user_not_found":
			continue
		case "conversation_not_found":
			continue
		case "not_in_conversation":
			continue
		default:
			return err
		}
	}
	return nil
}
