package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/igoracmelo/euperturbot/config"
	"github.com/igoracmelo/euperturbot/db"
	"github.com/igoracmelo/euperturbot/oai"
	"github.com/igoracmelo/euperturbot/tg"
	"github.com/igoracmelo/euperturbot/util"
)

type Handler struct {
	DB      *db.DB
	OAI     *oai.Client
	BotInfo *tg.User
	Config  *config.Config
}

func (h Handler) SubToTopic(bot *tg.Bot, u tg.Update) error {
	fields := strings.SplitN(u.Message.Text, " ", 2)
	topics := []string{}
	if len(fields) > 1 {
		topics = strings.Split(fields[1], "\n")
	}

	if len(topics) == 0 {
		return tg.SendMessageParams{
			Text: "cadê o(s) tópico(s)?",
		}
	}

	if len(topics) > 3 {
		return tg.SendMessageParams{
			Text: "no máximo 3 tópicos por vez",
		}
	}

	user := db.User{
		ID:        u.Message.From.ID,
		FirstName: sanitizeUsername(u.Message.From.FirstName),
		Username:  sanitizeUsername(u.Message.From.Username),
	}

	if u.Message.ReplyToMessage != nil {
		if u.Message.ReplyToMessage.From.IsBot {
			return tg.SendMessageParams{
				Text: "bot nao pode man",
			}
		}
		user.ID = u.Message.ReplyToMessage.From.ID
		user.FirstName = sanitizeUsername(u.Message.ReplyToMessage.From.FirstName)
		user.Username = sanitizeUsername(u.Message.ReplyToMessage.From.Username)
	}

	err := h.DB.SaveUser(user)
	if err != nil {
		return err
	}

	for i, topic := range topics {
		topics[i] = strings.TrimSpace(topic)
		topic := topics[i]

		if err := validateTopic(topic); err != nil {
			return err
		}

		exists, err := h.DB.ExistsChatTopic(u.Message.Chat.ID, topic)
		if err != nil {
			return err
		}

		if !exists && u.Message.From.ID != h.Config.GodID {
			return tg.SendMessageParams{
				Text: "macaquearam demais... chega!",
			}
		}

		userTopic := db.UserTopic{
			ChatID: u.Message.Chat.ID,
			UserID: user.ID,
			Topic:  topic,
		}
		err = h.DB.SaveUserTopic(userTopic)
		if err != nil {
			log.Print(err)
			return tg.SendMessageParams{
				Text: "falha ao salvar tópico " + topic,
			}
		}
	}

	txt := fmt.Sprintf("inscrições adicionadas para %s:\n", user.Name())
	for _, topic := range topics {
		txt += fmt.Sprintf("- %s\n", topic)
	}
	return tg.SendMessageParams{
		Text: txt,
	}
}

func (h Handler) UnsubTopic(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	topic := ""
	if len(fields) > 1 {
		topic = fields[1]
	}

	if err := validateTopic(topic); err != nil {
		return err
	}

	n, err := h.DB.DeleteUserTopic(db.UserTopic{
		ChatID: u.Message.Chat.ID,
		UserID: u.Message.From.ID,
		Topic:  topic,
	})
	if err != nil {
		return fmt.Errorf("falha ao descer :/ (%w)", err)
	}

	user, err := h.DB.FindUser(u.Message.From.ID)
	if err != nil {
		return err
	}

	if n == 0 {
		return tg.SendMessageParams{
			Text: fmt.Sprintf("usuário %s não está inscrito nesse tópico", user.Name()),
		}
	}

	return tg.SendMessageParams{
		Text: "inscrição removida para " + user.Name(),
	}
}

func (h Handler) CreatePoll(bot *tg.Bot, u tg.Update) error {
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
		IsAnonymous: util.ToPtr(false),
	})
	return err
}

func (h Handler) CallSubs(bot *tg.Bot, u tg.Update) error {
	log.Print(username(u.Message.From) + ": " + u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	topic := ""
	if len(fields) > 1 {
		topic = fields[1]
	}

	if err := validateTopic(topic); err != nil {
		return tg.SendMessageParams{
			Text: err.Error(),
		}
	}

	return h.callSubs(bot, u, topic, false)
}

func (h Handler) ListSubs(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	fields := strings.SplitN(u.Message.Text, " ", 2)
	topic := ""
	if len(fields) > 1 {
		topic = fields[1]
	}

	if err := validateTopic(topic); err != nil {
		return tg.SendMessageParams{
			ChatID: u.Message.Chat.ID,
			Text:   err.Error(),
		}
	}

	users, err := h.DB.FindUsersByTopic(u.Message.Chat.ID, topic)
	if err != nil {
		return tg.SendMessageParams{
			Text: "falha ao listar usuários",
		}
	}

	if len(users) == 0 {
		return tg.SendMessageParams{
			Text: "não tem ninguém inscrito nesse tópico",
		}
	}

	txt := fmt.Sprintf("*inscritos \\(%d\\)*\n", len(users))
	for _, user := range users {
		txt += fmt.Sprintf("\\- %s\n", user.Name())
	}
	return tg.SendMessageParams{
		Text:      txt,
		ParseMode: "MarkdownV2",
	}
}

func (h Handler) ListUserTopics(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	topics, err := h.DB.FindUserChatTopics(u.Message.Chat.ID, u.Message.From.ID)
	if err != nil {
		return tg.SendMessageParams{
			Text: "falha ao listar tópicos",
		}
	}

	if len(topics) == 0 {
		return tg.SendMessageParams{
			Text: "você não está inscrito em nenhum tópico",
		}
	}

	txt := "seus tópicos:\n"
	for _, topic := range topics {
		txt += fmt.Sprintf("(%02d)  %s\n", topic.Subscribers, topic.Topic)
	}

	return tg.SendMessageParams{
		Text: txt,
	}
}

func (h Handler) ListChatTopics(bot *tg.Bot, u tg.Update) error {
	log.Print(u.Message.Text)

	topics, err := h.DB.FindChatTopics(u.Message.Chat.ID)
	if err != nil {
		log.Print(err)
		return tg.SendMessageParams{
			Text: "falha ao listar tópicos",
		}
	}

	if len(topics) == 0 {
		return tg.SendMessageParams{
			Text: "não existe nenhum tópico registrado nesse chat",
		}
	}

	txt := "tópicos:\n"
	for _, topic := range topics {
		txt += fmt.Sprintf("- (%02d)  %s\n", topic.Subscribers, topic.Topic)
	}

	return tg.SendMessageParams{
		Text: txt,
	}
}

func (h Handler) CountEvent(bot *tg.Bot, u tg.Update) error {
	fields := strings.SplitN(u.Message.Text, " ", 2)
	if len(fields) == 1 {
		return tg.SendMessageParams{
			Text: "faltando nome do evento",
		}
	}

	event := db.ChatEvent{
		ChatID: u.Message.Chat.ID,
		Name:   strings.TrimSpace(fields[1]),
	}

	if u.Message.ReplyToMessage != nil {
		event.MsgID = u.Message.ReplyToMessage.MessageID
		event.Time = time.Unix(u.Message.ReplyToMessage.Date, 0)
		if u.Message.From.ID != h.Config.GodID {
			return tg.SendMessageParams{
				Text: "sai maluco",
			}
		}

		err := h.DB.SaveChatEvent(event)
		return err
	}

	events, err := h.DB.FindChatEventsByName(event.ChatID, event.Name)
	if err != nil {
		return err
	}

	if len(events) == 0 {
		return tg.SendMessageParams{
			Text: fmt.Sprintf("%s 0 vez(es)", event.Name),
		}
	}

	last := time.Since(events[0].Time)
	relative := util.RelativeDuration(last)

	var txt string
	if len(events) == 1 {
		txt = fmt.Sprintf("%s %d vez há %s", event.Name, len(events), relative)
	} else {
		txt = fmt.Sprintf("%s %d vezes. última vez há %s", event.Name, len(events), relative)
	}

	return tg.SendMessageParams{
		Text: txt,
	}
}

func (h Handler) UncountEvent(bot *tg.Bot, u tg.Update) error {
	fields := strings.SplitN(u.Message.Text, " ", 2)
	if len(fields) == 1 {
		return tg.SendMessageParams{
			Text: "faltando nome do evento",
		}
	}

	if u.Message.ReplyToMessage == nil {
		return tg.SendMessageParams{
			Text: "responda a mensagem que quer descontar",
		}
	}

	if u.Message.From.ID != h.Config.GodID {
		return tg.SendMessageParams{
			Text: "já disse pra sair, maluco",
		}
	}

	event := db.ChatEvent{
		ChatID: u.Message.Chat.ID,
		MsgID:  u.Message.ReplyToMessage.MessageID,
		Name:   strings.TrimSpace(fields[1]),
	}

	err := h.DB.DeleteChatEvent(event)
	if err != nil {
		return err
	}

	return tg.SendMessageParams{
		Text: "descontey",
	}
}

func (h Handler) SaveAudio(bot *tg.Bot, u tg.Update) error {
	if u.Message.ReplyToMessage == nil {
		return tg.SendMessageParams{
			Text: "responda ao audio que quer salvar",
		}
	}

	if u.Message.ReplyToMessage.Voice == nil {
		return tg.SendMessageParams{
			Text: "tem que ser uma mensagem de voz",
		}
	}

	err := h.DB.SaveVoice(db.Voice{
		FileID: u.Message.ReplyToMessage.Voice.FileID,
		UserID: u.Message.ReplyToMessage.From.ID,
	})
	if err != nil {
		return err
	}

	return tg.SendMessageParams{
		Text: "áudio salvo",
	}
}

func (h Handler) SendRandomAudio(bot *tg.Bot, u tg.Update) error {
	voice, err := h.DB.FindRandomVoice()
	if errors.Is(err, sql.ErrNoRows) {
		return tg.SendMessageParams{
			Text: "nenhum áudio salvo para mandar",
		}
	}
	if err != nil {
		return err
	}
	_, err = bot.SendVoice(tg.SendVoiceParams{
		ChatID:           u.Message.Chat.ID,
		Voice:            voice.FileID,
		ReplyToMessageID: u.Message.MessageID,
	})
	return err
}

func (h Handler) GPTCompletion(bot *tg.Bot, u tg.Update) error {
	chunks := strings.SplitN(u.Message.Text, " ", 2)
	if len(chunks) != 2 {
		return tg.SendMessageParams{
			ReplyToMessageID: u.Message.MessageID,
			Text:             "faltou a pergunta",
		}
	}

	name := username(u.Message.From)

	msg, err := bot.SendMessage(tg.SendMessageParams{
		ChatID:           u.Message.Chat.ID,
		ReplyToMessageID: u.Message.MessageID,
		Text:             "Carregando...",
	})
	if err != nil {
		return err
	}

	resp, err := h.OAI.Completion(&oai.CompletionParams{
		Messages: []oai.Message{
			{
				Content: fmt.Sprintf(
					"Meu nome é %s. Me responda com @%s.\n%s",
					name,
					name,
					chunks[1],
				),
			},
		},
	})
	if errors.Is(err, oai.ErrRateLimit) {
		_, err = bot.EditMessageText(tg.EditMessageTextParams{
			ChatID:    u.Message.Chat.ID,
			MessageID: msg.MessageID,
			Text:      "ignorated kk rate limit",
		})
		return err
	}
	if err != nil {
		_, _ = bot.EditMessageText(tg.EditMessageTextParams{
			ChatID:    u.Message.Chat.ID,
			MessageID: msg.MessageID,
			Text:      "vish deu ruim",
		})
		return err
	}

	_, err = bot.EditMessageText(tg.EditMessageTextParams{
		ChatID:    u.Message.Chat.ID,
		MessageID: msg.MessageID,
		Text:      resp.Choices[0].Message.Content,
		ParseMode: "MarkdownV2",
	})

	var boterr tg.BotError
	if errors.As(err, &boterr); boterr.Status == http.StatusBadRequest {
		_, err = bot.EditMessageText(tg.EditMessageTextParams{
			ChatID:    u.Message.Chat.ID,
			MessageID: msg.MessageID,
			Text:      resp.Choices[0].Message.Content,
		})
	}

	return err
}

func (h Handler) GPTChatCompletion(bot *tg.Bot, u tg.Update) error {
	// if u.Message.From.ID != godID {
	// 	return tg.SendMessageParams{
	// 		ReplyToMessageID: u.Message.MessageID,
	// 		Text:             "desculpe mas nao posso te deixar gastar os creditos do igorcafe",
	// 	}
	// }

	chunks := strings.SplitN(u.Message.Text, " ", 2)
	if len(chunks) != 2 {
		return tg.SendMessageParams{
			ReplyToMessageID: u.Message.MessageID,
			Text:             "faltou a pergunta",
		}
	}

	date := time.Unix(u.Message.Date, 0)
	if u.Message.ReplyToMessage != nil {
		date = time.Unix(u.Message.ReplyToMessage.Date, 0)
	}

	msgs, err := h.DB.FindMessagesBeforeDate(u.Message.Chat.ID, date, 100)
	if err != nil {
		return err
	}

	name := username(u.Message.From)
	title := u.Message.Chat.Title
	if title == "" {
		title = u.Message.Chat.FirstName
	}

	prompts := []oai.Message{
		{
			Content: fmt.Sprintf(
				"Mensagens recentes do chat %s para voce se contextualizar, no formato '<usuario>: <texto>'\n\n%s",
				title,
				strings.Join(prepareTextForGPT(msgs), "\n"),
			),
		},
		{
			Content: fmt.Sprintf(
				"Me chame de @%s e responda a mensagem abaixo. Se baseie no historico de mensagens acima e nos nomes de usuarios para responde. NÃO crie diálogos, apenas me responda com as informações fornecidas. As palavras 'grupo', 'chat', 'conversa', 'historico' todas se referecem ao historico do chat %s acima. Nao mencione o nome do grupo. Responda a seguinte me mencionando em segunda pessoa, usando @%s\n\n%s",
				name,
				title,
				name,
				chunks[1],
			),
		},
	}

	// for _, p := range prompts {
	// 	fmt.Println(p)
	// 	fmt.Println()
	// }

	msg, err := bot.SendMessage(tg.SendMessageParams{
		ChatID:           u.Message.Chat.ID,
		ReplyToMessageID: u.Message.MessageID,
		Text:             "Carregando...",
	})
	if err != nil {
		return err
	}

	resp, err := h.OAI.Completion(&oai.CompletionParams{
		Messages:    prompts,
		Temperature: 0.5,
	})
	if errors.Is(err, oai.ErrRateLimit) {
		return tg.SendMessageParams{
			ReplyToMessageID: u.Message.MessageID,
			Text:             "ignorated kk rate limit",
		}
	}
	if err != nil {
		return err
	}

	_, err = bot.EditMessageText(tg.EditMessageTextParams{
		ChatID:    u.Message.Chat.ID,
		MessageID: msg.MessageID,
		Text:      resp.Choices[0].Message.Content,
	})
	return err
}

func (h Handler) callSubs(bot *tg.Bot, u tg.Update, topic string, quiet bool) error {
	users, err := h.DB.FindUsersByTopic(u.Message.Chat.ID, topic)
	if err != nil {
		if quiet {
			return err
		}
		return tg.SendMessageParams{
			Text: "falha ao listar usuários",
		}
	}

	if len(users) == 0 {
		if quiet {
			return nil
		}
		return tg.SendMessageParams{
			Text: "não tem ninguém inscrito nesse tópico",
		}
	}

	txt := fmt.Sprintf(
		"*sim \\(0 votos\\)*\n\n"+
			"*não \\(0 votos\\)*\n\n"+
			"*restam \\(%d votos\\)*\n",
		len(users),
	)

	for _, u := range users {
		txt += fmt.Sprintf("[%s](tg://user?id=%d)\n", u.Name(), u.ID)
	}

	up := "👍 0"
	down := "👎 0"

	msg, err := bot.SendMessage(tg.SendMessageParams{
		ChatID:           u.Message.Chat.ID,
		Text:             txt,
		ParseMode:        "MarkdownV2",
		ReplyToMessageID: u.Message.MessageID,
		ReplyMarkup: &tg.InlineKeyboardMarkup{
			InlineKeyboard: [][]tg.InlineKeyboardButton{{
				tg.InlineKeyboardButton{
					Text:         up,
					CallbackData: "0",
				},
				tg.InlineKeyboardButton{
					Text:         down,
					CallbackData: "1",
				},
			}},
		},
	})
	if err != nil {
		return err
	}

	err = h.DB.SavePoll(db.Poll{
		ID:              strconv.Itoa(msg.MessageID),
		ChatID:          u.Message.Chat.ID,
		Topic:           topic,
		ResultMessageID: msg.MessageID,
	})

	return err
}

func (h Handler) CallbackQuery(bot *tg.Bot, u tg.Update) error {
	var err error

	poll, err := h.DB.FindPollByMessage(u.CallbackQuery.Message.MessageID)
	if err != nil {
		return err
	}

	voteNum, err := strconv.Atoi(u.CallbackQuery.Data)
	if err != nil {
		return err
	}

	// TODO: improve this logic
	vote, err := h.DB.FindPollVote(poll.ID, u.CallbackQuery.From.ID)
	if errors.Is(err, sql.ErrNoRows) {
		vote = nil
	} else if err != nil {
		return err
	}

	if vote != nil && vote.Vote == voteNum {
		err = h.DB.DeletePollVote(vote.PollID, vote.UserID)
	} else {
		err = h.DB.SavePollVote(db.PollVote{
			PollID: poll.ID,
			UserID: u.CallbackQuery.From.ID,
			Vote:   voteNum,
		})
	}
	if err != nil {
		return err
	}

	users, err := h.DB.FindUsersByTopic(poll.ChatID, poll.Topic)
	if err != nil {
		return err
	}

	found := false
	for _, user := range users {
		if user.ID == u.CallbackQuery.From.ID {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	positiveCount := 0
	positives := ""
	negativeCount := 0
	negatives := ""
	remainingCount := 0
	remainings := ""

	for _, user := range users {
		mention := fmt.Sprintf("[%s](tg://user?id=%d)\n", user.Name(), user.ID)

		vote, err := h.DB.FindPollVote(poll.ID, user.ID)
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

	up := "👍 " + fmt.Sprint(positiveCount)
	down := "👎 " + fmt.Sprint(negativeCount)

	_, err = bot.EditMessageText(tg.EditMessageTextParams{
		ChatID:    poll.ChatID,
		MessageID: poll.ResultMessageID,
		Text:      txt,
		ParseMode: "MarkdownV2",
		ReplyMarkup: &tg.InlineKeyboardMarkup{
			InlineKeyboard: [][]tg.InlineKeyboardButton{{
				tg.InlineKeyboardButton{
					Text:         up,
					CallbackData: "0",
				},
				tg.InlineKeyboardButton{
					Text:         down,
					CallbackData: "1",
				},
			}},
		},
	})
	return err
}

func (h Handler) Text(bot *tg.Bot, u tg.Update) error {
	txt := u.Message.Text
	if strings.HasPrefix(txt, "/") {
		return nil
	}

	name := username(u.Message.From)
	id := u.Message.From.ID

	if u.Message.FowardFrom != nil {
		name = username(u.Message.FowardFrom) + "(forward)"
		id = u.Message.FowardFrom.ID
	}

	if u.Message.ForwardSenderName != "" {
		id = 0
		name = sanitizeUsername(u.Message.ForwardSenderName)
	}

	replyID := 0
	if u.Message.ReplyToMessage != nil {
		replyID = u.Message.ReplyToMessage.MessageID
	}

	err := h.DB.SaveMessage(db.Message{
		ID:               u.Message.MessageID,
		ReplyToMessageID: replyID,
		ChatID:           u.Message.Chat.ID,
		Text:             txt,
		Date:             time.Unix(u.Message.Date, 0),
		UserID:           id,
		UserName:         name,
	})

	return err
}

var messageCount atomic.Int32

func (h Handler) Message(bot *tg.Bot, u tg.Update) error {
	t := u.Message.Text
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "#") {
		if err := validateTopic(t); err != nil {
			return nil
		}
		return h.callSubs(bot, u, t, true)
	}

	date := time.Unix(u.Message.Date, 0)
	if time.Since(date).Minutes() > 1 {
		return nil
	}

	n := messageCount.Add(1)
	const target = 150
	if n > target {
		messageCount.Store(0)
	}

	if n%10 == 0 {
		fmt.Printf("%d messages remaining\n", target-n)
	}

	if messageCount.CompareAndSwap(target, 0) {
		voice, err := h.DB.FindRandomVoice()
		if err != nil {
			return err
		}
		_, err = bot.SendVoice(tg.SendVoiceParams{
			ChatID:           u.Message.Chat.ID,
			Voice:            voice.FileID,
			ReplyToMessageID: u.Message.MessageID,
		})
		return err
	}

	return nil
}

// TODO:
func (h Handler) InlineQuery(bot *tg.Bot, u tg.Update) error {
	var err error

	// TODO: debounce by u.InlineQuery.ID
	util.Debounce(5*time.Second, func() {
		var resp *oai.CompletionResponse
		resp, err = h.OAI.Completion(&oai.CompletionParams{
			WaitRateLimit: true,
			Messages: []oai.Message{
				{
					Content: u.InlineQuery.Query,
				},
			},
		})

		if err != nil {
			_ = bot.AnswerInlineQuery(tg.AnswerInlineQueryParams{
				InlineQueryID: u.InlineQuery.ID,
				Results: []tg.InlineQueryResult{
					{
						Type:  "article",
						ID:    "1",
						Title: "Erro ao perguntar ao ChatGPT",
						InputMessageContent: tg.InputMessageContent{
							MessageText: "",
						},
					},
				},
			})
			return
		}

		title := resp.Choices[0].Message.Content
		if len(title) > 100 {
			title = title[:97] + "..."
		}

		err = bot.AnswerInlineQuery(tg.AnswerInlineQueryParams{
			InlineQueryID: u.InlineQuery.ID,
			Results: []tg.InlineQueryResult{
				{
					Type:  "article",
					ID:    fmt.Sprintf("%016X", rand.Int63()),
					Title: title,
					InputMessageContent: tg.InputMessageContent{
						MessageText: resp.Choices[0].Message.Content,
					},
				},
			},
		})
	})()

	return err
}

func prepareTextForGPT(msgs []db.Message) []string {
	msgTxts := []string{}
	totalLen := 0

	reURL := regexp.MustCompile(`https?:\/\/\S+`)
	reMultiSpace := regexp.MustCompile(`\s+`)
	reLaugh := regexp.MustCompile(`([kK]{7})[kK]+`)

	for i := len(msgs) - 1; i >= 0; i-- {
		txt := msgs[i].UserName + ": " + msgs[i].Text
		txt = reLaugh.ReplaceAllString(txt, "$1")
		txt = reURL.ReplaceAllString(txt, "")
		txt = reMultiSpace.ReplaceAllString(txt, " ")
		totalLen += len(txt)
		if totalLen > 2000 {
			break
		}
		msgTxts = append(msgTxts, txt)
	}

	for i, j := 0, len(msgTxts)-1; i < j; i, j = i+1, j-1 {
		msgTxts[i], msgTxts[j] = msgTxts[j], msgTxts[i]
	}

	return msgTxts
}
func sanitizeUsername(name string) string {
	s := ""
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			s += string(r)
		}
	}
	return strings.TrimSpace(s)
}

func username(user *tg.User) string {
	if user == nil {
		return ""
	}
	if user.Username != "" {
		return sanitizeUsername(user.Username)
	}
	return sanitizeUsername(user.FirstName)
}

func validateTopic(topic string) error {
	topic = strings.TrimSpace(topic)
	if len(topic) == 0 {
		return fmt.Errorf("tópico vazio")
	}
	if len(topic) > 30 {
		return fmt.Errorf("tópico muito grande")
	}
	if strings.Contains(topic, "\n") {
		return fmt.Errorf("tópico não pode ter mais de uma linha")
	}
	if strings.Contains(topic, "#") && strings.Contains(topic, " ") {
		return fmt.Errorf("tópico com # não pode ter espaço")
	}
	return nil
}