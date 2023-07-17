package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/glebarez/go-sqlite"
	"github.com/igoracmelo/euperturbot/dao"
	"github.com/igoracmelo/euperturbot/env"
	"github.com/igoracmelo/euperturbot/tg"
	sqlite3 "modernc.org/sqlite/lib"
)

var token string
var godID int64
var mydao *dao.DAO

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	token = env.Must("TOKEN")
	godID = env.MustInt64("GOD_ID")

	var err error
	mydao, err = dao.NewSqlite("euperturbot.db")
	if err != nil {
		panic(err)
	}

	bot := tg.NewBot(token)
	if err != nil {
		panic(err)
	}

	_, err = bot.GetMe()
	if err != nil {
		panic(err)
	}

	updates := bot.GetUpdatesChannel()
	h := tg.NewUpdateHandler(bot, updates)

	h.HandleCommand("suba", handleSubTopic)
	h.HandleCommand("desca", handleUnsubTopic)
	h.HandleCommand("pollo", handleCreatePoll)
	h.HandleCommand("bora", handleCallSubs)
	h.HandleCommand("quem", handleListSubs)
	h.HandleCommand("lista", handleListUserTopics)
	h.HandleCommand("listudo", handleListChatTopics)
	h.HandleCommand("conta", handleCountEvent)
	h.HandleCommand("desconta", handleUncountEvent)
	h.HandlePollAnswer(handlePollAnswer)
	h.Start()
}

func handleSubTopic(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	topic := ""
	if len(fields) > 1 {
		topic = fields[1]
	}

	if err := validateTopic(topic); err != nil {
		return err
	}

	exists, err := mydao.ExistsChatTopic(u.Message.Chat.ID, topic)
	if err != nil {
		return err
	}

	if !exists && u.Message.From.ID != godID {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "macaquearam demais... chega!",
		})
		return err
	}

	user := dao.User{
		ID:        u.Message.From.ID,
		FirstName: sanitizeUsername(u.Message.From.FirstName),
		Username:  sanitizeUsername(u.Message.From.Username),
	}

	if u.Message.ReplyToMessage != nil {
		if u.Message.ReplyToMessage.From.IsBot {
			_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
				Text: "bot nao pode man",
			})
			return err
		}
		user.ID = u.Message.ReplyToMessage.From.ID
		user.FirstName = sanitizeUsername(u.Message.ReplyToMessage.From.FirstName)
		user.Username = sanitizeUsername(u.Message.ReplyToMessage.From.Username)
	}

	err = mydao.SaveUser(user)
	if err != nil {
		return err
	}

	userTopic := dao.UserTopic{
		ChatID: u.Message.Chat.ID,
		UserID: user.ID,
		Topic:  topic,
	}
	err = mydao.SaveUserTopic(userTopic)
	if err, ok := err.(*sqlite.Error); ok &&
		err.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "já inscrito nesse tópico",
		})
		return err
	}
	if err != nil {
		fmt.Println(err)
		_, _ = replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "falha ao salvar tópico",
		})
		return err
	}

	_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
		Text: "inscrição adicionada para " + user.Name(),
	})
	return err
}

func handleUnsubTopic(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	topic := ""
	if len(fields) > 1 {
		topic = fields[1]
	}

	if err := validateTopic(topic); err != nil {
		return err
	}

	err := mydao.DeleteUserTopic(dao.UserTopic{
		ChatID: u.Message.Chat.ID,
		UserID: u.Message.From.ID,
		Topic:  topic,
	})
	if err != nil {
		return fmt.Errorf("falha ao descer :/ (%w)", err)
	}

	_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
		Text: "inscrição removida para o tópico " + topic,
	})
	return err
}

func handleCreatePoll(bot *tg.Bot, u tg.Update) error {
	log.Print(username(u.Message.From) + ": " + u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	question := ""
	if len(fields) > 1 {
		question = strings.TrimSpace(fields[1])
	}

	if question == "" {
		return fmt.Errorf("cade o titulo joe")
	}

	_, err := bot.SendPoll(tg.SendPollParams{
		ChatID:      u.Message.Chat.ID,
		Question:    question,
		Options:     []string{"👍🏿", "👎🏻"},
		IsAnonymous: tg.ToPtr(false),
	})
	return err
}

func handleListSubs(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	topic := ""
	if len(fields) > 1 {
		topic = fields[1]
	}

	if err := validateTopic(topic); err != nil {
		return err
	}

	users, err := mydao.FindUsersByTopic(u.Message.Chat.ID, topic)
	if err != nil {
		_, _ = replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "falha ao listar usuários",
		})
		return err
	}

	if len(users) == 0 {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "não tem ninguém inscrito nesse tópico",
		})
		return err
	}

	txt := fmt.Sprintf("*inscritos \\(%d\\)*\n", len(users))
	for _, user := range users {
		txt += fmt.Sprintf("\\- %s\n", user.Name())
	}
	_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
		Text:      txt,
		ParseMode: "MarkdownV2",
	})
	return err
}

func handleCallSubs(bot *tg.Bot, u tg.Update) error {
	log.Print(username(u.Message.From) + ": " + u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	topic := ""
	if len(fields) > 1 {
		topic = fields[1]
	}

	if err := validateTopic(topic); err != nil {
		_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: err.Error(),
		})
		return err
	}

	users, err := mydao.FindUsersByTopic(u.Message.Chat.ID, topic)
	if err != nil {
		_, _ = replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "falha ao listar usuários",
		})
		return err
	}

	if len(users) == 0 {
		_, _ = replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "não tem ninguém inscrito nesse tópico",
		})
		return err
	}

	msg, err := bot.SendPoll(tg.SendPollParams{
		ChatID:      u.Message.Chat.ID,
		Question:    topic,
		Options:     []string{"bo 👍🏿", "bo nao 👎🏻"},
		IsAnonymous: tg.ToPtr(false),
	})
	if err != nil {
		return err
	}
	poll := msg.Poll

	txt := fmt.Sprintf("*sim \\(0 votos\\)*\n\n"+
		"*não \\(0 votos\\)*\n\n"+
		"*restam \\(%d votos\\)*\n", len(users))

	for _, u := range users {
		txt += fmt.Sprintf("[%s](tg://user?id=%d)\n", u.Name(), u.ID)
	}

	msg, err = bot.SendMessage(tg.SendMessageParams{
		ChatID:    u.Message.Chat.ID,
		Text:      txt,
		ParseMode: "MarkdownV2",
	})
	if err != nil {
		return err
	}

	err = mydao.SavePoll(dao.Poll{
		ID:              poll.ID,
		ChatID:          u.Message.Chat.ID,
		Topic:           topic,
		ResultMessageID: msg.MessageID,
	})
	if err != nil {
		return err
	}

	return err
}

func handleListUserTopics(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	topics, err := mydao.FindUserChatTopics(u.Message.Chat.ID, u.Message.From.ID)
	if err != nil {
		return fmt.Errorf("falha ao listar tópicos")
	}

	if len(topics) == 0 {
		return fmt.Errorf("você não está inscrito em nenhum tópico")
	}

	txt := "seus tópicos:\n"
	for _, topic := range topics {
		txt += fmt.Sprintf("- (%02d)  %s\n", topic.Subscribers, topic.Topic)
	}

	_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
		Text: txt,
	})
	return err
}

func handleListChatTopics(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	topics, err := mydao.FindChatTopics(u.Message.Chat.ID)
	if err != nil {
		_, _ = replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "falha ao listar tópicos",
		})
		return err
	}

	if len(topics) == 0 {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "não existe nenhum tópico registrado nesse chat",
		})
		return err
	}

	txt := "tópicos:\n"
	for _, topic := range topics {
		txt += fmt.Sprintf("- (%02d)  %s\n", topic.Subscribers, topic.Topic)
	}

	_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
		Text: txt,
	})
	return err
}

func handleCountEvent(bot *tg.Bot, u tg.Update) error {
	fields := strings.SplitN(u.Message.Text, " ", 2)
	if len(fields) == 1 {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "faltando nome do evento",
		})
		return err
	}

	event := dao.ChatEvent{
		ChatID: u.Message.Chat.ID,
		Name:   strings.TrimSpace(fields[1]),
	}

	if u.Message.ReplyToMessage != nil {
		event.MsgID = u.Message.ReplyToMessage.MessageID
		event.Time = time.Unix(u.Message.ReplyToMessage.Date, 0)
		if u.Message.From.ID != godID {
			_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
				Text: "sai macaco",
			})
			return err
		}

		err := mydao.SaveChatEvent(event)
		return err
	}

	events, err := mydao.FindChatEventsByName(event.ChatID, event.Name)
	if err != nil {
		return err
	}

	if len(events) == 0 {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: fmt.Sprintf("%s 0 vez(es)", event.Name),
		})
		return err
	}

	last := time.Now().Sub(events[0].Time)
	relative := relativeDuration(last)

	var txt string
	if len(events) == 1 {
		txt = fmt.Sprintf("%s %d vez há %s", event.Name, len(events), relative)
	} else {
		txt = fmt.Sprintf("%s %d vezes. última vez há %s", event.Name, len(events), relative)
	}

	_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
		Text: txt,
	})
	return err
}

func handleUncountEvent(bot *tg.Bot, u tg.Update) error {
	fields := strings.SplitN(u.Message.Text, " ", 2)
	if len(fields) == 1 {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "faltando nome do evento",
		})
		return err
	}

	if u.Message.ReplyToMessage == nil {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "responda a mensagem que quer descontar",
		})
		return err
	}

	if u.Message.From.ID != godID {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "já disse pra sair, macaco",
		})
		return err
	}

	event := dao.ChatEvent{
		ChatID: u.Message.Chat.ID,
		MsgID:  u.Message.ReplyToMessage.MessageID,
		Name:   strings.TrimSpace(fields[1]),
	}

	err := mydao.DeleteChatEvent(event)
	if err != nil {
		return err
	}

	_, err = replyToMessage(bot, u.Message, &tg.SendMessageParams{
		Text: "descontey",
	})
	return err
}

func handleSpam(bot *tg.Bot, u tg.Update) error {
	panic("TODO")
	// if u.Message.From.ID != godID {
	// 	_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
	// 		Text: "sai man so faço isso pro @igorcafe",
	// 	})
	// 	return err
	// }

	fields := strings.SplitN(u.Message.Text, " ", 3)
	if len(fields) != 3 {
		_, err := replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: "uso: /spam <quantidade> <mensagem>",
		})
		return err
	}

	count, err := strconv.Atoi(fields[1])
	if err != nil {
		_, _ = replyToMessage(bot, u.Message, &tg.SendMessageParams{
			Text: fmt.Sprintf("quantidade inválida: '%s'", fields[1]),
		})
		return err
	}

	limit := make(chan struct{}, 10)

	for i := 0; i < count; i++ {
		limit <- struct{}{}
		go func() {
			// _, err = bot.SendMessage(tg.SendMessageParams{
			// 	ChatID: u.Message.Chat.ID,
			// 	Text:   fields[2],
			// })
			// if err != nil {
			// 	log.Print(err)
			// }
			<-limit
		}()
	}
	return nil
}

func handlePollAnswer(bot *tg.Bot, u tg.Update) error {
	var err error

	if len(u.PollAnswer.OptionIDs) == 0 {
		err = mydao.DeletePollVote(u.PollAnswer.PollID, u.PollAnswer.User.ID)
	} else {
		err = mydao.SavePollVote(dao.PollVote{
			PollID: u.PollAnswer.PollID,
			UserID: u.PollAnswer.User.ID,
			Vote:   u.PollAnswer.OptionIDs[0],
		})
	}
	if err != nil {
		return err
	}

	poll, err := mydao.FindPoll(u.PollAnswer.PollID)
	if err != nil {
		return err
	}

	users, err := mydao.FindUsersByTopic(poll.ChatID, poll.Topic)
	if err != nil {
		return err
	}

	positiveCount := 0
	positives := ""
	negativeCount := 0
	negatives := ""
	remainingCount := 0
	remainings := ""

	for _, u := range users {
		mention := fmt.Sprintf("[%s](tg://user?id=%d)\n", u.Name(), u.ID)

		vote, err := mydao.FindPollVote(poll.ID, u.ID)
		if errors.Is(err, sql.ErrNoRows) {
			remainings += mention
			remainingCount++
			continue
		} else if err != nil {
			return err
		}

		const yes = 0
		const no = 1

		if vote.Vote == yes {
			positiveCount++
			positives += mention
		} else if vote.Vote == no {
			negativeCount++
			negatives += mention
		}
	}

	txt := fmt.Sprintf(
		"*sim \\(%d votos\\)*\n%s\n*não \\(%d votos\\)*\n%s\n*restam \\(%d votos\\)*\n%s",
		positiveCount,
		positives,
		negativeCount,
		negatives,
		remainingCount,
		remainings,
	)

	_, err = bot.EditMessageText(tg.EditMessageTextParams{
		ChatID:    poll.ChatID,
		MessageID: poll.ResultMessageID,
		Text:      txt,
		ParseMode: "MarkdownV2",
	})
	return err
}

func handleAnyMessage(bot *tg.Bot, u tg.Update) {
	log.Printf("any text: %s", u.Message.Text)

	questions := []string{"and", "e?", "askers", "askers?", "perguntadores", "perguntadores?"}
	found := false
	for _, q := range questions {
		if u.Message.Text == q {
			found = true
			break
		}
	}
	if !found {
		return
	}

	msgID := 0
	if u.Message.ReplyToMessage != nil {
		msgID = u.Message.ReplyToMessage.MessageID
	}
	_, _ = bot.SendMessage(tg.SendMessageParams{
		ChatID:                   u.Message.Chat.ID,
		Text:                     "perguntadores not found",
		ReplyToMessageID:         msgID,
		AllowSendingWithoutReply: true,
	})
}

func replyToMessage(bot *tg.Bot, msg *tg.Message, params *tg.SendMessageParams) (*tg.Message, error) {
	if params == nil {
		params = &tg.SendMessageParams{}
	}

	params.ChatID = msg.Chat.ID
	params.ReplyToMessageID = msg.MessageID

	return bot.SendMessage(*params)
}

func validateTopic(topic string) error {
	topic = strings.TrimSpace(topic)
	if len(topic) == 0 {
		return fmt.Errorf("tópico vazio")
	}
	if len(topic) > 30 {
		return fmt.Errorf("tópico muito grande")
	}
	return nil
}

func sanitizeUsername(topic string) string {
	s := ""
	for _, r := range topic {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			s += string(r)
		}
	}
	return strings.TrimSpace(s)
}

func username(user *tg.User) string {
	s := ""
	if user.Username != "" {
		s = sanitizeUsername(user.Username)
	} else {
		s = sanitizeUsername(user.FirstName)
	}
	return s
}

func relativeDuration(d time.Duration) string {
	times := []string{}

	durationFormats := []struct {
		nameSingular string
		namePlural   string
		duration     time.Duration
	}{
		{"dia", "dias", 24 * time.Hour},
		{"hora", "horas", time.Hour},
		{"minuto", "minutos", time.Minute},
		{"segundo", "segundos", time.Second},
	}

	for _, format := range durationFormats {
		if len(times) == 2 {
			break
		}
		div := d / format.duration
		if div == 0 {
			continue
		}
		d -= div * format.duration

		s := fmt.Sprint(int(div)) + " "
		if div == 1 {
			s += format.nameSingular
		} else {
			s += format.namePlural
		}
		times = append(times, s)
	}

	return strings.Join(times, " e ")
}
