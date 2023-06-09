package dao

import (
	"database/sql"
	"log"
	"time"
)

type DAO struct {
	db *sql.DB
}

func NewSqlite(dsn string) (*DAO, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	err = db.Ping()
	if err != nil {
		return nil, err
	}

	migrations := `

		CREATE TABLE IF NOT EXISTS user_topic (
			id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			username TEXT NOT NULL,
			topic TEXT NOT NULL,
			UNIQUE(chat_id, user_id, topic)
		);

		CREATE TABLE IF NOT EXISTS event (
			id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			time TIMESTAMP NOT NULL,
			name TEXT NOT NULL,
			msg_id INTEGER NOT NULL,
			UNIQUE(chat_id, msg_id, name)
		);

		CREATE TABLE IF NOT EXISTS poll (
			id TEXT PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			topic TEXT NOT NULL,
			result_message_id INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS poll_vote (
			poll_id TEXT NOT NULL,
			user_id INTEGER NOT NULL,
			vote INTEGER NOT NULL,
			FOREIGN KEY (poll_id) REFERENCES poll(id),
			PRIMARY KEY(poll_id, user_id)
		);
	`

	_, err = db.Exec(migrations)
	if err != nil {
		return nil, err
	}
	return &DAO{db}, nil
}

func (dao *DAO) Close() error {
	return dao.db.Close()
}

func querySlice[E any](db *sql.DB, query string, args []any, dest func(*E) []any) ([]E, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}

	defer func() {
		err := rows.Close()
		if err != nil {
			log.Print(err)
		}
	}()

	var res []E

	for rows.Next() {
		var e E
		err := rows.Scan(dest(&e)...)
		if err != nil {
			return nil, err
		}
		res = append(res, e)
	}

	return res, rows.Err()
}

type UserTopic struct {
	ID       int64
	ChatID   int64
	UserID   int64
	Username string
	Topic    string
}

func (dao *DAO) ExistsChatTopic(chatID int64, topic string) (bool, error) {
	row := dao.db.QueryRow(`
		SELECT EXISTS (
			SELECT * FROM user_topic
			WHERE chat_id = $1 AND topic = $2
		)
	`, chatID, topic)

	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

func (dao *DAO) SaveUserTopic(topic UserTopic) error {
	_, err := dao.db.Exec(`
		INSERT INTO user_topic
		(chat_id, user_id, username, topic)
		VALUES ($1, $2, $3, $4)
	`, topic.ChatID, topic.UserID, topic.Username, topic.Topic)

	return err
}

func (dao *DAO) DeleteUserTopic(topic UserTopic) error {
	_, err := dao.db.Exec(`
		DELETE FROM user_topic
		WHERE chat_id = $1 AND user_id = $2 AND topic = $3
	`, topic.ChatID, topic.UserID, topic.Topic)

	return err
}

func (dao *DAO) FindUserChatTopics(chatID, userID int64) ([]UserTopic, error) {
	sql := `
		SELECT * FROM user_topic
		WHERE chat_id = $1 AND user_id = $2
	`
	return querySlice[UserTopic](
		dao.db,
		sql,
		[]any{chatID, userID},
		func(t *UserTopic) []any {
			return []any{&t.ID, &t.ChatID, &t.UserID, &t.Username, &t.Topic}
		},
	)
}

type nextFn func() nextFn

func (dao *DAO) FindChatTopics(chatID int64) ([]UserTopic, error) {
	sql := `
		SELECT DISTINCT * FROM user_topic
		WHERE chat_id = $1
		GROUP BY topic
	`
	return querySlice[UserTopic](
		dao.db,
		sql,
		[]any{chatID},
		func(t *UserTopic) []any {
			return []any{&t.ID, &t.ChatID, &t.UserID, &t.Username, &t.Topic}
		},
	)
}

func (dao *DAO) FindSubscriptionsByTopic(chatID int64, topic string) ([]UserTopic, error) {
	sql := `
		SELECT * FROM user_topic
		WHERE chat_id = $1 AND topic = $2
	`
	return querySlice[UserTopic](
		dao.db,
		sql,
		[]any{chatID, topic},
		func(t *UserTopic) []any {
			return []any{&t.ID, &t.ChatID, &t.UserID, &t.Username, &t.Topic}
		},
	)
}

type ChatEvent struct {
	ID     int64
	ChatID int64
	MsgID  int
	Time   time.Time
	Name   string
}

func (dao *DAO) SaveChatEvent(e ChatEvent) error {
	_, err := dao.db.Exec(`
		INSERT INTO event
		(chat_id, msg_id, time, name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO UPDATE SET time = $3
	`, e.ChatID, e.MsgID, e.Time, e.Name)

	return err
}

func (dao *DAO) FindChatEventsByName(chatID int64, name string) ([]ChatEvent, error) {
	sql := `
		SELECT id, chat_id, msg_id, time, name FROM event
		WHERE chat_id = $1 AND name = $2
		ORDER BY time DESC
	`
	return querySlice[ChatEvent](
		dao.db,
		sql,
		[]any{chatID, name},
		func(e *ChatEvent) []any {
			return []any{&e.ID, &e.ChatID, &e.MsgID, &e.Time, &e.Name}
		},
	)
}

func (dao *DAO) DeleteChatEvent(e ChatEvent) error {
	_, err := dao.db.Exec(`
		DELETE FROM event
		WHERE chat_id = $1 AND msg_id = $2 AND name = $3
	`, e.ChatID, e.MsgID, e.Name)
	return err
}

type Poll struct {
	ID              string
	ChatID          int64
	Topic           string
	ResultMessageID int
}

func (dao *DAO) SavePoll(p Poll) error {
	_, err := dao.db.Exec(`
		INSERT INTO poll
		(id, chat_id, topic, result_message_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO UPDATE SET result_message_id = $4
	`, p.ID, p.ChatID, p.Topic, p.ResultMessageID)
	return err
}

func (dao *DAO) FindPoll(pollID string) (*Poll, error) {
	row := dao.db.QueryRow(`
		SELECT * FROM poll
		WHERE id = $1
	`, pollID)

	var p Poll
	err := row.Scan(&p.ID, &p.ChatID, &p.Topic, &p.ResultMessageID)
	return &p, err
}

type PollVote struct {
	PollID   string
	UserID   int64
	Username string
	Vote     int
}

func (dao *DAO) SavePollVote(v PollVote) error {
	_, err := dao.db.Exec(`
		INSERT INTO poll_vote
		(poll_id, user_id, vote)
		VALUES ($1, $2, $3)
		ON CONFLICT DO UPDATE SET vote = $3
	`, v.PollID, v.UserID, v.Vote)

	return err
}

func (dao *DAO) FindPollVotes(pollID string) ([]PollVote, error) {
	sql := `
		SELECT v.poll_id, v.user_id, u.username, v.vote FROM poll_vote AS v
		WHERE poll_id = $1
	`
	return querySlice[PollVote](
		dao.db,
		sql,
		[]any{pollID},
		func(v *PollVote) []any {
			return []any{&v.PollID, &v.UserID, &v.Username, &v.Vote}
		},
	)
}
