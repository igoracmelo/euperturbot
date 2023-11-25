package db

import (
	"database/sql"
	"log"
	"sync"
	"time"
)

type DB struct {
	db      *sql.DB
	stmts   map[string]*sql.Stmt
	stmtsMu *sync.RWMutex
}

type ColumnMapper interface {
	ColumnMap() map[string]any
}

func NewSqlite(dsn string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	err = db.Ping()
	if err != nil {
		return nil, err
	}

	return &DB{
		db,
		make(map[string]*sql.Stmt),
		new(sync.RWMutex),
	}, nil
}

func (db *DB) Close() error {
	return db.db.Close()
}

func (db *DB) prepareCached(query string) (*sql.Stmt, error) {
	db.stmtsMu.RLock()
	stmt, ok := db.stmts[query]
	db.stmtsMu.RUnlock()

	if ok {
		return stmt, nil
	}

	stmt, err := db.db.Prepare(query)
	if err != nil {
		return nil, err
	}

	db.stmtsMu.Lock()
	db.stmts[query] = stmt
	db.stmtsMu.Unlock()

	return stmt, nil
}

func (db *DB) execStmt(query string, args ...any) (sql.Result, error) {
	stmt, err := db.prepareCached(query)
	if err != nil {
		return nil, err
	}
	return stmt.Exec(args...)
}

func (db *DB) queryStmt(query string, args ...any) (*sql.Rows, error) {
	stmt, err := db.prepareCached(query)
	if err != nil {
		return nil, err
	}
	return stmt.Query(args...)
}

func scanCols(rows *sql.Rows, colNames []string, entity ColumnMapper) error {
	dest := make([]any, len(colNames))
	var discard any
	for i := range dest {
		dest[i] = &discard
	}

	m := entity.ColumnMap()
	for i, col := range colNames {
		ptr, ok := m[col]
		if ok {
			dest[i] = ptr
		}
	}

	return rows.Scan(dest...)
}

func queryRow(db *sql.DB, dest ColumnMapper, query string, args ...any) error {
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	if !rows.Next() {
		err = rows.Err()
		if err == nil {
			return sql.ErrNoRows
		}
		return err
	}

	err = scanCols(rows, cols, dest)
	return err
}

func querySlice[E any](db *sql.DB, query string, args []any, dest func(*E) map[string]any) ([]E, error) {
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

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var res []E

	for rows.Next() {
		var e E
		m := dest(&e)
		var discard any
		args := make([]any, len(cols))
		for i := range args {
			args[i] = &discard
		}
		for i, col := range cols {
			if ptr, ok := m[col]; ok {
				args[i] = ptr
			}
		}
		err := rows.Scan(args...)
		if err != nil {
			return nil, err
		}
		res = append(res, e)
	}

	return res, rows.Err()
}

type User struct {
	ID        int64
	FirstName string
	Username  string
}

func (u *User) Name() string {
	if u.Username != "" {
		return u.Username
	}
	return u.FirstName
}

func (u *User) ColumnMap() map[string]any {
	return map[string]any{
		"id":         &u.ID,
		"username":   &u.Username,
		"first_name": &u.FirstName,
	}
}

func (db *DB) SaveUser(u User) error {
	_, err := db.db.Exec(`
		INSERT INTO user
		(id, first_name, username)
		VALUES ($1, $2, $3)
		ON CONFLICT DO UPDATE
		SET first_name = $2, username = $3
	`, u.ID, u.FirstName, u.Username)

	return err
}

func (db *DB) FindUser(id int64) (*User, error) {
	var u User
	err := queryRow(db.db, &u, `SELECT * FROM user WHERE id = $1`, id)
	return &u, err
}

type UserTopic struct {
	ID          int64
	ChatID      int64
	UserID      int64
	Topic       string
	Subscribers int
}

func (db *DB) ExistsChatTopic(chatID int64, topic string) (bool, error) {
	row := db.db.QueryRow(`
		SELECT EXISTS (
			SELECT * FROM user_topic
			WHERE chat_id = $1 AND topic = $2
		)
	`, chatID, topic)

	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

func (db *DB) SaveUserTopic(topic UserTopic) error {
	_, err := db.db.Exec(`
		INSERT INTO user_topic
		(chat_id, user_id, topic)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, topic.ChatID, topic.UserID, topic.Topic)

	return err
}

func (db *DB) DeleteUserTopic(topic UserTopic) (int64, error) {
	res, err := db.db.Exec(`
		DELETE FROM user_topic
		WHERE chat_id = $1 AND user_id = $2 AND topic = $3
	`, topic.ChatID, topic.UserID, topic.Topic)

	if err != nil {
		return 0, err
	}

	return res.RowsAffected()
}

func (db *DB) FindUserChatTopics(chatID, userID int64) ([]UserTopic, error) {
	sql := `
		SELECT *, (
			SELECT COUNT(*) FROM user_topic
			WHERE chat_id = $1 AND topic = ut.topic
			GROUP BY topic
		) AS subscribers
		FROM user_topic ut
		WHERE chat_id = $1 AND user_id = $2
	`
	return querySlice[UserTopic](
		db.db,
		sql,
		[]any{chatID, userID},
		func(t *UserTopic) map[string]any {
			return map[string]any{
				"id":          &t.ID,
				"chat_id":     &t.ChatID,
				"user_id":     &t.UserID,
				"topic":       &t.Topic,
				"subscribers": &t.Subscribers,
			}
		},
	)
}

func (db *DB) FindChatTopics(chatID int64) ([]UserTopic, error) {
	sql := `
		SELECT DISTINCT *, COUNT(*) AS subscribers FROM user_topic
		WHERE chat_id = $1
		GROUP BY topic, chat_id
		ORDER BY subscribers DESC
	`
	return querySlice[UserTopic](
		db.db,
		sql,
		[]any{chatID},
		func(t *UserTopic) map[string]any {
			return map[string]any{
				"id":          &t.ID,
				"chat_id":     &t.ChatID,
				"user_id":     &t.UserID,
				"topic":       &t.Topic,
				"subscribers": &t.Subscribers,
			}
		},
	)
}

func (db *DB) FindUsersByTopic(chatID int64, topic string) ([]User, error) {
	sql := `
		SELECT u.* FROM user u
		JOIN user_topic ut ON u.id = ut.user_id
		WHERE ut.chat_id = $1 AND ut.topic = $2
	`
	return querySlice[User](
		db.db,
		sql,
		[]any{chatID, topic},
		func(u *User) map[string]any {
			return map[string]any{
				"id":         &u.ID,
				"username":   &u.Username,
				"first_name": &u.FirstName,
			}
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

func (db *DB) SaveChatEvent(e ChatEvent) error {
	_, err := db.db.Exec(`
		INSERT INTO event
		(chat_id, msg_id, time, name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO UPDATE SET time = $3
	`, e.ChatID, e.MsgID, e.Time, e.Name)

	return err
}

func (db *DB) FindChatEventsByName(chatID int64, name string) ([]ChatEvent, error) {
	sql := `
		SELECT * FROM event
		WHERE chat_id = $1 AND name = $2
		ORDER BY time DESC
	`
	return querySlice[ChatEvent](
		db.db,
		sql,
		[]any{chatID, name},
		func(e *ChatEvent) map[string]any {
			return map[string]any{
				"id":      &e.ID,
				"chat_id": &e.ChatID,
				"msg_id":  &e.MsgID,
				"time":    &e.Time,
				"name":    &e.Name,
			}
		},
	)
}

func (db *DB) DeleteChatEvent(e ChatEvent) error {
	_, err := db.db.Exec(`
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

func (p *Poll) ColumnMap() map[string]any {
	return map[string]any{
		"id":                &p.ID,
		"chat_id":           &p.ChatID,
		"topic":             &p.Topic,
		"result_message_id": &p.ResultMessageID,
	}
}

func (db *DB) SavePoll(p Poll) error {
	_, err := db.db.Exec(`
		INSERT INTO poll
		(id, chat_id, topic, result_message_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO UPDATE SET result_message_id = $4
	`, p.ID, p.ChatID, p.Topic, p.ResultMessageID)
	return err
}

func (db *DB) FindPoll(pollID string) (*Poll, error) {
	var p Poll
	err := queryRow(db.db, &p, `SELECT * FROM poll WHERE id = $1`, pollID)
	return &p, err
}

func (db *DB) FindPollByMessage(msgID int) (*Poll, error) {
	var p Poll
	err := queryRow(db.db, &p, `SELECT * FROM poll WHERE result_message_id = $1`, msgID)
	return &p, err
}

func (db *DB) FindLastPollByTopic(topic string) (*Poll, error) {
	var p Poll
	err := queryRow(db.db, &p, `
		SELECT * FROM poll
		WHERE topic = $1
		ORDER BY result_message_id DESC
	`, topic)
	return &p, err
}

type PollVote struct {
	PollID string
	UserID int64
	Vote   int
}

func (v *PollVote) ColumnMap() map[string]any {
	return map[string]any{
		"poll_id": &v.PollID,
		"user_id": &v.UserID,
		"vote":    &v.Vote,
	}
}

func (db *DB) SavePollVote(v PollVote) error {
	_, err := db.db.Exec(`
		INSERT INTO poll_vote
		(poll_id, user_id, vote)
		VALUES ($1, $2, $3)
		ON CONFLICT DO UPDATE SET vote = $3
	`, v.PollID, v.UserID, v.Vote)

	return err
}

func (db *DB) DeletePollVote(pollID string, userID int64) error {
	_, err := db.db.Exec(`
		DELETE FROM poll_vote
		WHERE poll_id = $1 AND user_id = $2
	`, pollID, userID)
	return err
}

func (db *DB) FindPollVotes(pollID string) ([]PollVote, error) {
	sql := `
		SELECT * FROM poll_vote
		WHERE poll_id = $1
	`
	return querySlice[PollVote](
		db.db,
		sql,
		[]any{pollID},
		func(e *PollVote) map[string]any {
			return map[string]any{
				"poll_id": &e.PollID,
				"user_id": &e.UserID,
				"vote":    &e.Vote,
			}
		},
	)
}

func (db *DB) FindPollVote(pollID string, userID int64) (*PollVote, error) {
	var v PollVote
	err := queryRow(db.db, &v, `
		SELECT * FROM poll_vote
		WHERE poll_id = $1 AND user_id = $2
	`, pollID, userID)
	return &v, err
}

type Voice struct {
	FileID string
	UserID int64
}

func (v *Voice) ColumnMap() map[string]any {
	return map[string]any{
		"file_id": &v.FileID,
		"user_id": &v.UserID,
	}
}

func (db *DB) SaveVoice(v Voice) error {
	_, err := db.db.Exec(`
		INSERT INTO voice (file_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, v.FileID, v.UserID)
	return err
}

func (db *DB) FindRandomVoice() (*Voice, error) {
	var v Voice
	err := queryRow(db.db, &v, `SELECT * FROM voice ORDER BY RANDOM() LIMIT 1`)
	return &v, err
}

type Message struct {
	ID       int
	Text     string
	Date     time.Time
	UserName string
}

func (db *DB) SaveMessage(msg Message) error {
	if len(msg.Text) > 500 {
		msg.Text = msg.Text[:500]
	}

	_, err := db.db.Exec(`
		INSERT INTO message (
			id,
			date,
			text,
			user_name
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO UPDATE SET text = $3
	`, msg.ID, msg.Date, msg.Text, msg.UserName)
	return err
}