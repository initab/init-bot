package memory

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"init-bot/types"
	"maunium.net/go/mautrix/id"
)

const TokenHandler = "TokenHandler"
const MessageHandler = "MessageHandler"

type Handler struct {
	Type           string                          `json:"type"`
	CurrentContext map[id.RoomID]types.RoomContext `json:"current_context"`
	Persist        bool                            `json:"persist"`
	ctx            context.Context
	db             *pgxpool.Pool
	logger         zerolog.Logger
}

func NewTokenHandler(ctx context.Context, db *pgxpool.Pool, log zerolog.Logger) *Handler {
	return &Handler{
		Type:           TokenHandler,
		CurrentContext: make(map[id.RoomID]types.RoomContext),
		Persist:        false,
		ctx:            ctx,
		db:             db,
		logger:         log.With().Str("component", "memory-handler").Logger(),
	}
}

func NewMessageHandler(ctx context.Context, db *pgxpool.Pool, log zerolog.Logger) *Handler {
	return &Handler{
		Type:           MessageHandler,
		CurrentContext: make(map[id.RoomID]types.RoomContext),
		Persist:        false,
		ctx:            ctx,
		db:             db,
		logger:         log.With().Str("component", "memory-handler").Logger(),
	}
}

func (mh *Handler) SetupDatabase() error {
	conn, err := mh.db.Acquire(mh.ctx)
	defer conn.Release()
	if err != nil {
		mh.logger.Warn().
			Err(err).
			Msg("Error connecting to Database")
		mh.Persist = false
		return err
	}

	// Taken from StackOverflow as how to actually check for Real Tables that the user can access
	rows, _ := conn.Query(mh.ctx, "SELECT EXISTS (SELECT FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE  n.nspname = $1 AND c.relname = $2 AND c.relkind = 'r');", "public", "Bot-Context")
	defer rows.Close()
	bools, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	rows.Close()

	// If the table doesn't exist in the database we create it. This is where the Bots "memory" will be stored in the form of LLM Tokens that makes up the LLM Context (Different from the Background Context used for all functions in this code)
	if !bools[0] {
		_, err := conn.Exec(mh.ctx, "CREATE TABLE \"Bot-Context\" (room text PRIMARY KEY, context integer ARRAY, messages jsonb)")
		if err != nil {
			mh.logger.Error().
				Err(err).
				Msg("Error creating Bot-Context table")
			mh.Persist = false
			return err
		}
	}
	mh.Persist = true
	return nil
}

func (mh *Handler) LoadContext(room id.RoomID) error {
	if !mh.Persist {
		mh.logger.Warn().Msg("Persistence is not turned on. Will return nothing")
		return nil
	}
	var returnContext types.RoomContext
	db, err := mh.db.Acquire(mh.ctx)
	defer db.Release()
	if err != nil {
		mh.logger.Error().
			Err(err).
			Msg("Error acquiring database connection")
		mh.Persist = false
		return err
	}
	row, err := db.Query(mh.ctx, "SELECT room, context, messages FROM \"Bot-Context\" WHERE room = $1", room.String())
	if err != nil {
		mh.logger.Error().
			Err(err).
			Msg("Error querying Bot-Context table")
		mh.Persist = false
		return err
	}
	for row.Next() {
		var roomContext []int
		var roomID string
		var roomMessages []types.Message
		err = row.Scan(&roomID, &roomContext, &roomMessages)
		if err != nil {
			mh.logger.Error().
				Err(err).
				Msg("Error scanning row context")
			mh.Persist = false
			return err
		}
		if mh.Type == TokenHandler {
			returnContext = types.RoomContext{Room: id.RoomID(roomID), Tokens: roomContext}
		} else if mh.Type == MessageHandler {
			returnContext = types.RoomContext{Room: id.RoomID(roomID), Messages: roomMessages}
		} else {
			mh.logger.Error().Msgf("Not implemented Memory Handler: %s", mh.Type)
			return errors.New("not implemented")
		}
		mh.CurrentContext[room] = returnContext
		//mh.logger.Debug().Msgf("Context for room ID %s is: %v", room, mh.CurrentContext[room])
	}
	return nil
}

func (mh *Handler) SaveContext(room id.RoomID) error {
	if !mh.Persist {
		mh.logger.Warn().Msg("Persistence is not turned on. Context will not persist!")
		return nil
	}
	db, err := mh.db.Acquire(mh.ctx)
	defer db.Release()
	if err != nil {
		mh.logger.Error().
			Err(err).
			Msg("Error acquiring database connection")
		mh.Persist = false
		return err
	}
	if mh.Type == TokenHandler {
		_, err = db.Exec(mh.ctx, "INSERT INTO \"Bot-Context\" (room, context) VALUES ($1, $2) ON CONFLICT(room) DO UPDATE SET context = EXCLUDED.context", room.String(), mh.CurrentContext[room].Tokens)
	} else if mh.Type == MessageHandler {
		jsonValue, err := json.Marshal(mh.CurrentContext[room].Messages)
		if err != nil {
			mh.logger.Error().Err(err).Msg("Error serializing context into JSON")
			return err
		}
		_, err = db.Exec(mh.ctx, "INSERT INTO \"Bot-Context\" (room, messages) VALUES ($1, $2) ON CONFLICT(room) DO UPDATE SET messages = EXCLUDED.messages", room.String(), jsonValue)
		//mh.logger.Debug().Msgf("Return value from inserting Message type of context: %v", ret)
		if err != nil {
			mh.logger.Error().
				Err(err).
				Msg("Error inserting contexts to database")
			mh.Persist = false
		}
	}

	return nil
}

func (mh *Handler) UpdateCurrentContext(room id.RoomID, context types.RoomContext) {
	currRoom := mh.CurrentContext[room]
	if mh.Type == TokenHandler {
		currRoom.Tokens = append(currRoom.Tokens, context.Tokens...)
	} else if mh.Type == MessageHandler {
		currRoom.Messages = append(currRoom.Messages, context.Messages...)
	}

	mh.CurrentContext[room] = currRoom
	//mh.logger.Debug().Msgf("Updated context for room ID %s with: %v", room, mh.CurrentContext[room])
}
