package main

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

// postFile streams from a regular client-local file. Closing the pipe and waiting
// for the producer also handles early rejection without leaving a blocked writer.
func postFile(client *http.Client, endpoint, chatID, as, path string) (*http.Response, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		_ = file.Close()
		return nil, errors.New("file must be a non-empty regular file")
	}
	reader, writer := io.Pipe()
	mw := multipart.NewWriter(writer)
	done := make(chan error, 1)
	go func() {
		writeErr := writeUpload(mw, file, chatID, as, filepath.Base(path))
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = writer.CloseWithError(writeErr)
		done <- writeErr
	}()
	resp, postErr := client.Post(endpoint, mw.FormDataContentType(), reader)
	_ = reader.Close()
	writeErr := <-done
	if postErr != nil {
		return nil, postErr
	}
	// Preserve the gateway's rejection rather than an incidental broken pipe.
	if resp.StatusCode == http.StatusOK && writeErr != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("upload file: %w", writeErr)
	}
	return resp, nil
}

func writeUpload(mw *multipart.Writer, file io.Reader, chatID, as, name string) error {
	if err := mw.WriteField("chat_id", chatID); err != nil {
		return err
	}
	if err := mw.WriteField("as", as); err != nil {
		return err
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	return mw.Close()
}
