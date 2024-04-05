package matrixbot

import (
	"context"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"slices"
)

func SetupSyncer(bot *MatrixBot) (MatrixBot, error) {
	// Setup new syncer using the Client set in the bot
	syncer := bot.Client.Syncer.(*mautrix.DefaultSyncer)

	//Handle messages send to the channel
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, ev *event.Event) {
		messageEvent := ev.Content.AsMessage()

		if messageEvent.MsgType == event.MsgNotice {
			log.Debug().Msg("Notice, do nothing")
			return
		}

		botUser := bot.Client.UserID

		if messageEvent.Mentions.UserIDs != nil && slices.Contains(messageEvent.Mentions.UserIDs, botUser) {
			log.Debug().
				Msgf("%s said \"%s\" in room %s", ev.Sender, messageEvent.Body, ev.RoomID)

			bot.handleCommands(bot.Context, messageEvent.Body, ev.RoomID, ev.Sender)
		} else {
			log.Debug().Msg("Message not for bot")
		}

	})

	//Handle member events (kick, invite)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, ev *event.Event) {
		eventMember := ev.Content.AsMember()
		log.Debug().
			Msgf("%s changed bot membership status in %s", ev.Sender, ev.RoomID)
		if eventMember.Membership.IsInviteOrJoin() {
			log.Debug().
				Msgf("Joining Room %s", ev.RoomID)
			if resp, err := bot.Client.JoinRoom(ctx, ev.RoomID.String(), "", nil); err != nil {
				log.Fatal().
					Str("error", err.Error()).
					Msg("Problem joining room")
			} else {
				log.Debug().
					Msgf("Joined room %s", resp.RoomID)
			}
		} else if eventMember.Membership.IsLeaveOrBan() {
			log.Debug().
				Msgf("Kicked from room %S", ev.RoomID)
			log.Info().
				Msgf("Kicked from %s for reason: %s", ev.RoomID, eventMember.Reason)
		}
	})

	return *bot, nil
}
