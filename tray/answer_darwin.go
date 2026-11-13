//go:build darwin

package tray

import "C"

import "strings"

// answerPrefix starts the id of an agent's answer's notification; the review's page follows.
const answerPrefix = "answer:"

// goNotificationAnswer is a notification's answer, or with text nil a click on the
// notification itself, which opens the answer dialog, or for an agent's answer its pull
// request's review. It runs on the main thread, so the work goes elsewhere.
//
//export goNotificationAnswer
func goNotificationAnswer(qid, text *C.char) {
	tr, id := active, C.GoString(qid)
	if tr == nil {
		return
	}
	if page, ok := strings.CutPrefix(id, answerPrefix); ok {
		go func() {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			tr.openPage(page)
		}()
		return
	}
	if text == nil {
		go tr.answerID(id)
		return
	}
	answer := C.GoString(text)
	go func() {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.t.Answer(id, answer)
		tr.refresh()
	}()
}
