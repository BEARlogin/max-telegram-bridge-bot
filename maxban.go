package main

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// maxBanClient — отдельный клиент с таймаутом для бана в MAX.
var maxBanClient = &http.Client{Timeout: 10 * time.Second}

// banMaxMember удаляет участника из MAX-чата С ЗАПРЕТОМ возврата (block=true).
// SDK v1.4.2 умеет только RemoveMember без блокировки (кикнутый может вернуться),
// а флаг block появился лишь в v2. Чтобы не мигрировать весь MAX-клиент ради одного
// метода, дёргаем REST напрямую — так же, как upload.go (Authorization: MaxToken).
func (b *Bridge) banMaxMember(ctx context.Context, chatID, userID int64) error {
	url := fmt.Sprintf("%schats/%d/members?user_id=%d&block=true&v=1.2.5", maxAPIBaseURL, chatID, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", b.cfg.MaxToken)
	resp, err := maxBanClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("max ban http %d", resp.StatusCode)
	}
	return nil
}
