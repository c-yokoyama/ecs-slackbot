package main

import (
	"encoding/json"
	"log"
	"net/url"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/kelseyhightower/envconfig"
	"github.com/nlopes/slack"
	"github.com/nlopes/slack/slackevents"
)

// Response is of type APIGatewayProxyResponse since we're leveraging the
// AWS Lambda Proxy Request functionality (default behavior)
//
// https://serverless.com/framework/docs/providers/aws/events/apigateway/#lambda-proxy-integration
type Response events.APIGatewayProxyResponse

type envConfig struct {
	BotUserToken      string `envconfig:"BOT_USER_OAUTH_TOKEN" required:"true"`
	VerificationToken string `envconfig:"VERIFICATION_TOKEN" required:"true"`
	Region            string `envconfig:"REGION" required:"true"`
}

var env envConfig

func getResponseMsg(code int, body string) Response {
	return Response{
		StatusCode:      code,
		IsBase64Encoded: false,
		Body:            body,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}
}

// format slack message for responding to Interactive Message
func getResponseInteractiveMsg(originalMsg slack.Message, text string, color string, attachmentActs []slack.AttachmentAction, fields []slack.AttachmentField, callbackID string) (string, error) {
	if text != "" {
		originalMsg.Msg.Attachments[0].Text = text
	}
	if color != "" {
		originalMsg.Msg.Attachments[0].Color = color
	}
	if callbackID != "" {
		originalMsg.Msg.Attachments[0].CallbackID = callbackID
	}
	originalMsg.Msg.Attachments[0].Actions = attachmentActs
	originalMsg.Attachments[0].Fields = fields
	originalMsg.Msg.ReplaceOriginal = true

	r, err := json.Marshal(originalMsg)
	if err != nil {
		log.Printf("[ERROR] json encode err, %v", err)
		return "", err
	}
	return string(r), nil
}

// Handler is our lambda handler invoked by the `lambda.Start` function call
func Handler(request events.APIGatewayProxyRequest) (Response, error) {
	// load lambda Environment Variales
	if err := envconfig.Process("", &env); err != nil {
		log.Printf("[ERROR] Failed to process envconfig: %s", err)
		return getResponseMsg(500, ""), err
	}

	log.Printf("[INFO] Events: %+v", request)
	body := request.Body

	// Handle Interactive User Response
	if strings.HasPrefix(body, "payload") {
		jsonStr, _ := url.QueryUnescape(body[8:])
		log.Printf("[INFO] payload: %s", jsonStr)

		var i *slack.InteractionCallback
		err := json.Unmarshal([]byte(jsonStr), &i)
		if err != nil {
			log.Printf("[ERROR] failed to decode json: %v", err)
			return getResponseMsg(500, ""), err
		}

		// User pushed cancel button
		if i.Actions[0].Name == "cancel" {
			r, err := getResponseInteractiveMsg(i.OriginalMessage, "", "#808080", []slack.AttachmentAction{}, []slack.AttachmentField{
				{
					Title: "@" + i.User.Name + " canceled.",
					Value: "",
					Short: false,
				},
			},
				"")
			if err != nil {
				return getResponseMsg(500, ""), err
			}
			return getResponseMsg(200, r), nil

			// User selected ECS Cluster name
		} else if i.Actions[0].Name == "clusters" {
			selectedCluster := i.Actions[0].SelectedOptions[0].Value
			services, err := ListEcsService(selectedCluster)
			if err != nil {
				return getResponseMsg(500, ""), err
			}
			var opts []slack.AttachmentActionOption
			for _, s := range services {
				opts = append(opts, slack.AttachmentActionOption{Text: s, Value: s})
			}

			r, err := getResponseInteractiveMsg(i.OriginalMessage, "Choose ECS Service in "+selectedCluster, "", []slack.AttachmentAction{
				{
					Name:    "services",
					Type:    "select",
					Options: opts,
				},
				{
					Name:  "cancel",
					Text:  "Cancel",
					Type:  "button",
					Style: "danger",
				},
			}, []slack.AttachmentField{}, selectedCluster)
			if err != nil {
				return getResponseMsg(500, ""), err
			}
			return getResponseMsg(200, r), nil

			// User selectecd ECS Service name
		} else if i.Actions[0].Name == "services" {
			// in this case, i.CallbackID has cluster name
			// ToDO: make suffix variable
			if !strings.HasSuffix(i.CallbackID, "-cluster") {
				log.Printf("[ERROR] cluster name suffix is unexpected. please see resource name.")
				return getResponseMsg(500, ""), err
			}

			service := i.Actions[0].SelectedOptions[0].Value
			// taskDefName: env prefix + service name
			// ToDO: fix to match other name
			taskDefName := strings.Split(i.CallbackID, "-cluster")[0] + "-" + service

			// Get task definition revisions and its image tags
			taskRevTags, err := ListTaskRevsAndImageTags(taskDefName)
			if err != nil {
				return getResponseMsg(500, ""), err
			}

			var opts []slack.AttachmentActionOption
			for _, t := range taskRevTags {
				taskRev := taskDefName + ":" + t.revNum
				opts = append(opts, slack.AttachmentActionOption{Text: taskRev + " | " + t.imageTag, Value: taskRev + "/" + service})
			}
			r, err := getResponseInteractiveMsg(i.OriginalMessage, "Choose image tag (commit hash)", "", []slack.AttachmentAction{
				{
					Name:    "imgTags",
					Type:    "select",
					Options: opts,
				},
				{
					Name:  "cancel",
					Text:  "Cancel",
					Type:  "button",
					Style: "danger",
				},
			}, []slack.AttachmentField{}, "")
			if err != nil {
				return getResponseMsg(500, ""), err
			}
			return getResponseMsg(200, r), nil

			// User selected ECS TaskDifinition + revision
		} else if i.Actions[0].Name == "imgTags" {
			// selectedTaskRevSvc <- "taskDefinition:Revision/servicename"
			selectedTaskRevSvc := i.Actions[0].SelectedOptions[0].Value
			r, err := getResponseInteractiveMsg(i.OriginalMessage, "Deploy "+strings.Split(selectedTaskRevSvc, "/")[0]+"?", "", []slack.AttachmentAction{
				{
					Name:  "taskStart",
					Text:  "Start",
					Value: selectedTaskRevSvc,
					Type:  "button",
					Style: "primary",
				},
				{
					Name:  "cancel",
					Text:  "Cancel",
					Type:  "button",
					Style: "danger",
				},
			}, []slack.AttachmentField{}, "")
			if err != nil {
				return getResponseMsg(500, ""), err
			}
			return getResponseMsg(200, r), nil

			// User pushed Deploy start button
		} else if i.Actions[0].Name == "taskStart" {
			cluster := i.CallbackID
			s := strings.Split(i.Actions[0].Value, "/")
			taskDefRev := s[0]
			service := s[1]

			if err := UpdateEcsService(cluster, service, taskDefRev); err != nil {
				return getResponseMsg(500, ""), err
			}
			r, err := getResponseInteractiveMsg(i.OriginalMessage, "", "#0174DF", []slack.AttachmentAction{}, []slack.AttachmentField{
				{
					Title: "@" + i.User.Name + " started deploy.",
					Value: "",
					Short: false,
				},
			}, "")
			if err != nil {
				return getResponseMsg(500, ""), err
			}
			return getResponseMsg(200, r), nil

		}
		// Unmatched any case
		return getResponseMsg(200, ""), nil
	}

	// Handle Event API requests
	verifyToken, err := DecodeString(env.VerificationToken)
	if err != nil {
		log.Printf("[ERROR] failed to decode encrypted env val with KMS %+v", err)
	}
	eventsAPIEvent, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionVerifyToken(&slackevents.TokenComparator{VerificationToken: verifyToken}))
	if err != nil {
		log.Printf("[ERROR] Failed to exec ParseEvent: %s", err)
		return getResponseMsg(500, ""), err
	}

	log.Printf("[DEBUG] eventsAPIEvent: %+v", eventsAPIEvent)

	// Slack EventAPI url_verification event
	if eventsAPIEvent.Type == slackevents.URLVerification {
		var r *slackevents.ChallengeResponse
		err := json.Unmarshal([]byte(body), &r)
		if err != nil {
			return getResponseMsg(500, ""), err
		}
		verifiResp := Response{
			StatusCode:      200,
			IsBase64Encoded: false,
			Body:            r.Challenge,
			Headers: map[string]string{
				"Content-Type": "text",
			},
		}
		return verifiResp, nil
	}

	// Handle user mention to bot
	if eventsAPIEvent.Type == slackevents.CallbackEvent {
		BotUserToken, err := DecodeString(env.BotUserToken)
		if err != nil {
			log.Printf("[ERROR] failed to decode encrypted env val with KMS %+v", err)
		}
		api := slack.New(BotUserToken)
		innerEvent := eventsAPIEvent.InnerEvent
		switch ev := innerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			m := strings.Split(strings.TrimSpace(ev.Text), " ")[1:]
			log.Printf("[INFO] BOT args: %v", m)

			if len(m) == 0 {
				// first, post ECS cluster select menu
				clusters, err := ListEcsCluster()
				if err != nil {
					return getResponseMsg(500, ""), err
				}

				var opts []slack.AttachmentActionOption
				for _, s := range clusters {
					opts = append(opts, slack.AttachmentActionOption{Text: s, Value: s})
				}

				attachment := slack.Attachment{
					Text:       "Choose ECS Cluster",
					Color:      "#ff8c00",
					CallbackID: "ecsdeploy",
					Actions: []slack.AttachmentAction{
						{
							Name:    "clusters",
							Type:    "select",
							Options: opts,
						},
						{
							Name:  "cancel",
							Text:  "Cancel",
							Type:  "button",
							Style: "danger",
						},
					},
				}
				api.PostMessage(ev.Channel, slack.MsgOptionAttachments(attachment))
				// Return Instance ids matched with prefix
			} else if len(m) == 1 && m[0] != "help" {
				instances, err := FilterInstances(m[0])
				if err != nil {
					return getResponseMsg(500, ""), err
				}
				log.Printf("[INFO] instances: %v", instances)
				fields := []slack.AttachmentField{}
				for _, i := range instances {
					fields = append(fields, slack.AttachmentField{
						Title: i.name,
						Value: i.id,
						Short: false,
					})
				}
				attachment := slack.Attachment{
					Pretext: "*Instance ID List in " + env.Region + "*",
					Color:   "#0174DF",
					Fields:  fields,
				}
				api.PostMessage(ev.Channel, slack.MsgOptionAttachments(attachment))
			} else {
				// unmatched any command
				helpMsg := "```\n"
				helpMsg += "# Usage\n"
				helpMsg += "@<bot-user-name> : ECS deploy interactive menu\n"
				helpMsg += "@<bot-user-name> <instance name prefix> : Return instance Ids\n"
				helpMsg += "```\n"
				api.PostMessage(ev.Channel, slack.MsgOptionText(helpMsg, false))
			}
		}
	}

	return getResponseMsg(200, ""), nil
}

func main() {
	lambda.Start(Handler)
}
