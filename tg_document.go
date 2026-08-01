package main

import maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"

// tgDocumentMaxSpec preserves the way the sender attached a file in Telegram.
// A video sent as Document must stay a downloadable file in MAX; only Telegram's
// Video field is relayed through MAX video processing.
func tgDocumentMaxSpec(fileName, mimeType string) (string, maxschemes.UploadType, string) {
	if fileName == "" {
		fileName = mimeToFilename("document", mimeType)
	}
	return fileName, maxschemes.FILE, "file"
}
