package matrixbot

import (
	"context"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"slices"
	"strings"
)

func SetupSyncer(bot *MatrixBot) (MatrixBot, error) {
	// Setup new syncer using the Client set in the bot
	syncer := bot.Client.Syncer.(*mautrix.DefaultSyncer)

	//Handle messages send to the channel
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, ev *event.Event) {
		messageEvent := ev.Content.AsMessage()

		if messageEvent.MsgType == event.MsgNotice {
			bot.Log.Debug().Msg("Notice, do nothing")
			return
		}

		botUser := bot.Client.UserID

		if hasMentions(messageEvent, botUser, bot.Name) {
			bot.Log.Debug().
				Msgf("%s said \"%s\" in room %s", ev.Sender, messageEvent.Body, ev.RoomID)

			bot.handleCommands(bot.Context, messageEvent.Body, ev.RoomID, ev.Sender)

		} else {
			bot.Log.Debug().Msg("Message not for bot")
		}

	})

	//Handle member events (kick, invite)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, ev *event.Event) {
		eventMember := ev.Content.AsMember()
		bot.Log.Debug().
			Msgf("%s changed bot membership status in %s", ev.Sender, ev.RoomID)
		if eventMember.Membership.IsInviteOrJoin() {
			bot.Log.Debug().
				Msgf("Joining Room %s", ev.RoomID)
			if resp, err := bot.Client.JoinRoom(ctx, ev.RoomID.String(), "", nil); err != nil {
				bot.Log.Fatal().
					Str("error", err.Error()).
					Msg("Problem joining room")
			} else {
				bot.Log.Debug().
					Msgf("Joined room %s", resp.RoomID)
			}
		} else if eventMember.Membership.IsLeaveOrBan() {
			bot.Log.Debug().
				Msgf("Kicked from room %S", ev.RoomID)
			bot.Log.Info().
				Msgf("Kicked from %s for reason: %s", ev.RoomID, eventMember.Reason)
		}
	})

	return *bot, nil
}

// hasMentions checks if a user has been mentioned in a message event.
// It looks for mentions in both the event's mentions and message body.
// If the user is found, it returns true; otherwise, it returns false.
func hasMentions(messageEvent *event.MessageEventContent, user id.UserID, userName string) bool {
	// We do this because there are currently two ways of mentioning users. Either in m.mentions or in m.message.body
	// hence we need to check both and also guard against the non-existence of m.mentions
	return (messageEvent.Mentions != nil &&
		messageEvent.Mentions.UserIDs != nil &&
		slices.Contains(messageEvent.Mentions.UserIDs, user)) || // end of modern way of mentioning -check
		strings.Contains(messageEvent.Body, userName)
}
