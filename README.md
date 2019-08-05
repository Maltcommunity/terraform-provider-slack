# Terraform Provider for Slack


This is a [Slack](https://slack.com) provider for [Terraform](https://www.terraform.io/)

(Forked https://github.com/TimDurward/terraform-provider-slack.git)

```hcl
provider "slack" {
  # Can also instead be provided through the API_TOKEN environment variable.
  # Note that SLACK_API_TOKEN is a user type token, the required scopes depends on which methods are called.
  api_token = "SLACK_API_TOKEN"
}

resource "slack_channel" "jenkins_ci" {
  channel_name = "jenkins"
  channel_topic = "Jenkins Integration for production deploys"
  # force_delete (optional, default: false)
  # requires the admin scope if set to true (default, will delete the channel in case of resource destruction)
  # requires the channels:write scope if set to false (will archive the channel in case of resource destruction)
  force_delete = false
}

resource "slack_conversation_members" "jenkins_ci" {
  conversation_id = "${slack_channel.jenkins_ci.id}"
  members = [
    "email:user@domain.com",
    "id:UXXXXXXXX" // must be a User ID
  ]
  # authoritative (optional, default: false)
  # if set to true (default: false), all members not present within the resource members attributes will be kicked out of the conversation. 
  # for public channels, it requires a token with the following scopes: channels:write, channel.read, users.read, users.read.email
  # for private channels (UNTESTED), requrires a token with the following scopes: groups:write, groups.read, users.read, users.read.email
  authoritative = true
}
```