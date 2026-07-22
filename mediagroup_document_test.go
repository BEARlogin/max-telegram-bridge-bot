package main

import "testing"

func TestTgMediaGroupDocumentPreservesPDF(t *testing.T) {
	msg := &TGMessage{Document: &DocInfo{
		FileID:   "pdf-file-id",
		FileName: "report.pdf",
		MimeType: "application/pdf",
	}}

	fileID, name := tgMediaGroupDocument(msg)
	if fileID != "pdf-file-id" || name != "report.pdf" {
		t.Fatalf("tgMediaGroupDocument() = %q, %q", fileID, name)
	}
}

func TestTgMediaGroupDocumentUsesMimeFallback(t *testing.T) {
	msg := &TGMessage{Document: &DocInfo{FileID: "pdf-file-id", MimeType: "application/pdf"}}

	fileID, name := tgMediaGroupDocument(msg)
	if fileID != "pdf-file-id" || name != "document.pdf" {
		t.Fatalf("tgMediaGroupDocument() = %q, %q", fileID, name)
	}
}
