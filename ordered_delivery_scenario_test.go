package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

// --- общая оснастка сценарных тестов порядка ---

type laneRoundTripper func(*http.Request) (*http.Response, error)

func (f laneRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (f laneRoundTripper) Do(req *http.Request) (*http.Response, error) { return f(req) }

func laneHTTPResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Body:          io.NopCloser(bytes.NewReader(body)),
		Header:        make(http.Header),
		ContentLength: int64(len(body)),
	}
}

// laneRequestBody читает тело запроса; у GET и у POST без тела его нет.
func laneRequestBody(req *http.Request) []byte {
	if req.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(req.Body)
	return body
}

// lanePNG — минимальная картинка: http.DetectContentType обязан увидеть image/png,
// иначе путь фото уйдёт в ветку GIF/документа и тест перестанет мерить, что задумано.
func lanePNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00}, 64)...)
}

// laneFileMarker — метка исходного file_id внутри «содержимого» файла TG.
const laneFileMarker = "tg-file-id:"

// laneFileBytes — файл, который отдаёт поддельный Telegram: картинка плюс метка
// file_id. По ней поддельный CDN MAX выдаёт СВОЙ токен на каждую часть альбома,
// иначе перестановку частей внутри альбома в отправленном сообщении не увидеть.
func laneFileBytes(fileID string) []byte {
	return append(lanePNG(), []byte(laneFileMarker+fileID)...)
}

// laneTokenFor достаёт метку из multipart-тела заливки и делает из неё токен.
func laneTokenFor(uploaded []byte) string {
	i := bytes.Index(uploaded, []byte(laneFileMarker))
	if i < 0 {
		return "tok-unknown"
	}
	rest := uploaded[i+len(laneFileMarker):]
	end := 0
	for end < len(rest) {
		c := rest[end]
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			break
		}
		end++
	}
	return "tok-" + string(rest[:end])
}

// deliveryLog — порядок доставок, наблюдаемый на границе с получателем.
type deliveryLog struct {
	mu    sync.Mutex
	items []string
}

func (l *deliveryLog) add(label string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, label)
}

func (l *deliveryLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.items...)
}

func (l *deliveryLog) waitFor(count int, timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	for {
		got := l.snapshot()
		if len(got) >= count || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// scenarioLanes — диспетчер для сценарных тестов. BRIDGE_LANE_MUTANT=1 включает
// режим «горутина на задачу»: это мутационный контроль, в нём сценарии ОБЯЗАНЫ
// падать, иначе они не проверяют порядок, а просто наблюдают удачное совпадение.
func scenarioLanes() *laneDispatcher {
	if os.Getenv("BRIDGE_LANE_MUTANT") == "1" {
		return newLaneDispatcher(unorderedLaneDispatch)
	}
	return newLaneDispatcher(nil)
}

func assertDeliveryOrder(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("доставлено %d из %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Fatalf("порядок доставки нарушен: получено %v, ожидалось по метке %v", got, want)
		}
	}
}

// --- MAX→TG ---

// orderedTGSender — поддельный TG, который держит фото дольше текста и записывает
// порядок отправок.
type orderedTGSender struct {
	TGSender
	log        *deliveryLog
	photoDelay time.Duration
	seq        int
	mu         sync.Mutex
}

func (s *orderedTGSender) nextID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

func (s *orderedTGSender) SendMessage(_ context.Context, _ int64, text string, _ *SendOpts) (int, error) {
	s.log.add("text:" + text)
	return s.nextID(), nil
}

func (s *orderedTGSender) SendPhoto(_ context.Context, _ int64, _ FileArg, _ *SendOpts) (int, error) {
	time.Sleep(s.photoDelay)
	s.log.add("photo")
	return s.nextID(), nil
}

func maxTextUpdate(maxChatID int64, mid, text string) *maxschemes.MessageCreatedUpdate {
	return &maxschemes.MessageCreatedUpdate{Message: maxschemes.Message{
		Sender:    maxschemes.User{UserId: 777, Name: "Автор"},
		Recipient: maxschemes.Recipient{ChatId: maxChatID, ChatType: maxschemes.CHAT},
		Body:      maxschemes.MessageBody{Mid: mid, Text: text},
	}}
}

func maxPhotoUpdate(maxChatID int64, mid, text, url string) *maxschemes.MessageCreatedUpdate {
	upd := maxTextUpdate(maxChatID, mid, text)
	upd.Message.Body.Attachments = []interface{}{
		&maxschemes.PhotoAttachment{Payload: maxschemes.PhotoAttachmentPayload{Url: url}},
	}
	return upd
}

func runMaxToTgBatch(t *testing.T) []string {
	t.Helper()
	repo := testRepo(t)
	const maxChat, tgChat = int64(9100), int64(-1009100)
	if _, err := repo.db.Exec(
		`INSERT INTO pairs (tg_chat_id, max_chat_id, prefix, created_at) VALUES (?,?,0,1)`,
		tgChat, maxChat); err != nil {
		t.Fatal(err)
	}

	log := &deliveryLog{}
	sender := &orderedTGSender{log: log, photoDelay: 400 * time.Millisecond}
	b := &Bridge{
		cfg:  Config{MaxToken: "test-token"},
		repo: repo,
		tg:   sender,
		httpClient: &http.Client{Transport: laneRoundTripper(func(*http.Request) (*http.Response, error) {
			return laneHTTPResponse(lanePNG()), nil
		})},
		apiClient:   &http.Client{},
		breakers:    make(map[int64]*chatBreaker),
		mgBuffers:   make(map[string]*mediaGroupBuffer),
		maxBotCache: make(map[int64]string),
		lanes:       scenarioLanes(),
	}

	ctx := context.Background()
	batch := []*maxschemes.MessageCreatedUpdate{
		maxTextUpdate(maxChat, "mid-1", "первый"),
		maxPhotoUpdate(maxChat, "mid-2", "с фото", "http://max.cdn/photo.png"),
		maxTextUpdate(maxChat, "mid-3", "последний"),
	}
	for _, upd := range batch {
		caption := formatMaxCaptionWithName(upd, b.maxRelayName(upd), false, b.cfg.MessageNewline)
		b.submitMaxToTg(ctx, upd, tgChat, caption, false)
	}

	return log.waitFor(len(batch), 30*time.Second)
}

// TestMaxToTgBatchKeepsOrder: пачка «текст, фото, текст» из одного MAX-чата должна
// прийти в TG в исходном порядке — фото задерживает следующий текст, а не наоборот.
func TestMaxToTgBatchKeepsOrder(t *testing.T) {
	assertDeliveryOrder(t, runMaxToTgBatch(t), []string{"первый", "photo", "последний"})
}

// --- TG→MAX ---

type orderedTGFileSender struct {
	TGSender
	slowFileID string
	delay      time.Duration
}

func (s *orderedTGFileSender) GetFile(_ context.Context, fileID string) (string, error) {
	return "files/" + fileID, nil
}

func (s *orderedTGFileSender) GetFileDirectURL(filePath string) string {
	return "http://tg.local/" + filePath
}

func (s *orderedTGFileSender) BotUsername() string { return "bridge_bot" }
func (s *orderedTGFileSender) BotID() int64        { return 1 }
func (s *orderedTGFileSender) BotToken() string    { return "tg-token" }

// tgPhotoMessage — часть альбома или одиночное фото БЕЗ автора: пустая подпись
// исключает из сценария доводку подписи альбома (отдельный механизм, к порядку
// доставки отношения не имеет).
func tgPhotoMessage(chatID int64, msgID int, fileID, mediaGroupID string) *TGMessage {
	return &TGMessage{
		MessageID:    msgID,
		Chat:         ChatInfo{ID: chatID, Type: "supergroup"},
		Photo:        []PhotoSize{{FileID: fileID}},
		MediaGroupID: mediaGroupID,
	}
}

func runTgToMaxBatch(t *testing.T) []string {
	t.Helper()
	repo := testRepo(t)
	const maxChat, tgChat = int64(9200), int64(-1009200)
	if _, err := repo.db.Exec(
		`INSERT INTO pairs (tg_chat_id, max_chat_id, prefix, created_at) VALUES (?,?,0,1)`,
		tgChat, maxChat); err != nil {
		t.Fatal(err)
	}

	log := &deliveryLog{}
	const slowPhotoID = "slow-photo"
	sender := &orderedTGFileSender{slowFileID: slowPhotoID, delay: 400 * time.Millisecond}

	// Скачивание файла из TG и заливка на CDN MAX: задержку даёт транспорт,
	// как в бою — медиа доходит медленнее текста.
	tgTransport := laneRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			// Ответ CDN MAX на multipart-заливку: токен свой у каждого файла.
			token := laneTokenFor(laneRequestBody(req))
			return laneHTTPResponse([]byte(`{"photos":{"p":{"token":"` + token + `"}}}`)), nil
		}
		fileID := path.Base(req.URL.Path)
		if fileID == sender.slowFileID {
			time.Sleep(sender.delay)
		}
		return laneHTTPResponse(laneFileBytes(fileID)), nil
	})

	// Прямые вызовы MAX API (получение upload-URL и отправка текста).
	apiTransport := laneRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := laneRequestBody(req)
		switch {
		case strings.Contains(req.URL.Path, "uploads"):
			return laneHTTPResponse([]byte(`{"url":"http://max.cdn/upload"}`)), nil
		case req.Method == http.MethodPost:
			log.add("text:" + string(body))
			return laneHTTPResponse([]byte(`{"message":{"body":{"mid":"max-text"}}}`)), nil
		default:
			// Правки (PUT) в порядок доставки не входят.
			return laneHTTPResponse([]byte(`{"success":true}`)), nil
		}
	})

	// Отправка медиа идёт через SDK MAX — его клиент подменяем целиком.
	sdkTransport := laneRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := laneRequestBody(req)
		if req.Method == http.MethodGet {
			// Проверка сохранённой подписи альбома: текст на месте, доводка не нужна.
			return laneHTTPResponse([]byte(`{"body":{"mid":"max-media","text":"подпись"}}`)), nil
		}
		log.add("media:" + string(body))
		return laneHTTPResponse([]byte(`{"message":{"body":{"mid":"max-media","text":"подпись"}}}`)), nil
	})
	maxApi, err := maxbot.New("test-token", maxbot.WithHTTPClient(sdkTransport))
	if err != nil {
		t.Fatal(err)
	}

	b := &Bridge{
		cfg:         Config{MaxToken: "test-token", TgAPIURL: "http://tg.local"},
		repo:        repo,
		tg:          sender,
		maxApi:      maxApi,
		httpClient:  &http.Client{Transport: tgTransport},
		apiClient:   &http.Client{Transport: apiTransport},
		breakers:    make(map[int64]*chatBreaker),
		mgBuffers:   make(map[string]*mediaGroupBuffer),
		maxBotCache: make(map[int64]string),
		lanes:       scenarioLanes(),
	}

	ctx := context.Background()
	messages := []*TGMessage{
		{MessageID: 1, Chat: ChatInfo{ID: tgChat, Type: "supergroup"},
			From: &UserInfo{ID: 42, FirstName: "Автор"}, Text: "первый"},
		tgPhotoMessage(tgChat, 2, slowPhotoID, ""),
		tgPhotoMessage(tgChat, 3, "album-1", "album-42"),
		tgPhotoMessage(tgChat, 4, "album-2", "album-42"),
		tgPhotoMessage(tgChat, 5, "album-3", "album-42"),
		{MessageID: 6, Chat: ChatInfo{ID: tgChat, Type: "supergroup"},
			From: &UserInfo{ID: 42, FirstName: "Автор"}, Text: "последний"},
	}
	for _, msg := range messages {
		caption := formatTgCaptionWithName(msg, b.tgRelayName(msg), false, b.cfg.MessageNewline)
		b.routeTgMessageToMax(ctx, msg, maxChat, caption)
	}

	// Четыре доставки: текст, одиночное фото, альбом одним сообщением, текст.
	return log.waitFor(4, 60*time.Second)
}

// TestTgToMaxBatchKeepsOrder: пачка «текст, медленное фото, альбом из 3 частей, текст»
// уходит в MAX в исходном порядке, а альбом остаётся одним сообщением на своём месте.
func TestTgToMaxBatchKeepsOrder(t *testing.T) {
	got := runTgToMaxBatch(t)
	assertDeliveryOrder(t, got, []string{"первый", "tok-slow-photo", "tok-album-", "последний"})

	album := got[2]
	// Альбом — одно сообщение с тремя вложениями, не три отдельных.
	if n := strings.Count(album, "tok-album-"); n != 3 {
		t.Fatalf("альбом собран не из трёх частей (%d): %s", n, album)
	}
	// Части внутри альбома идут в порядке получения: буферизация синхронная.
	positions := make([]int, 0, 3)
	for _, part := range []string{"tok-album-1", "tok-album-2", "tok-album-3"} {
		at := strings.Index(album, part)
		if at < 0 {
			t.Fatalf("в альбоме нет части %s: %s", part, album)
		}
		positions = append(positions, at)
	}
	if positions[0] > positions[1] || positions[1] > positions[2] {
		t.Fatalf("части альбома перепутаны (позиции %v): %s", positions, album)
	}
}
