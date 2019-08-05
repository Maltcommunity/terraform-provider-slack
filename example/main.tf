# Terraform Slack Provider
# Example
provider "slack" {
  api_token = "SLACK_API_TOKEN"
}

resource "slack_channel" "public-channel" {
  channel_name = "public-channel"
}

resource "slack_conversation_members" "public-channel" {
  conversation_id = "${slack_channel.public-channel.id}"
  members = [
    "email:user@domain.com",
    "id:UXXXXXXXX"
  ]
  authoritative = false
}

resource "slack_conversation_members" "private-channel" {
  conversation_id = "XXXXXXXXX"
  members = [
    "email:jane@domain.com",
    "email:john@domain.com"
  ]
  authoritative = true
}