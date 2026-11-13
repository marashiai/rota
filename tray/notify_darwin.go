//go:build darwin

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#include <stdlib.h>
#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>

extern void goNotificationAnswer(char *qid, char *text);

static volatile int atAllowed = -1; // 1 allowed, 0 not, -1 not known yet, 2 the system refused this app

@interface ATDelegate : NSObject <UNUserNotificationCenterDelegate>
@end
@implementation ATDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center willPresentNotification:(UNNotification *)n
	withCompletionHandler:(void (^)(UNNotificationPresentationOptions))done {
	done(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList | UNNotificationPresentationOptionSound);
}
- (void)userNotificationCenter:(UNUserNotificationCenter *)center didReceiveNotificationResponse:(UNNotificationResponse *)r
	withCompletionHandler:(void (^)(void))done {
	char *qid = (char *)r.notification.request.identifier.UTF8String;
	if ([r isKindOfClass:[UNTextInputNotificationResponse class]]) {
		goNotificationAnswer(qid, (char *)((UNTextInputNotificationResponse *)r).userText.UTF8String);
	} else if ([r.actionIdentifier isEqualToString:UNNotificationDefaultActionIdentifier]) {
		goNotificationAnswer(qid, NULL);
	}
	done();
}
@end

static ATDelegate *atDelegate;

static void setupNotifications(void) {
	UNUserNotificationCenter *c = [UNUserNotificationCenter currentNotificationCenter];
	atDelegate = [ATDelegate new];
	c.delegate = atDelegate;
	UNTextInputNotificationAction *answer = [UNTextInputNotificationAction actionWithIdentifier:@"answer" title:@"Answer"
		options:0 textInputButtonTitle:@"Send" textInputPlaceholder:@"Your answer"];
	[c setNotificationCategories:[NSSet setWithObject:[UNNotificationCategory categoryWithIdentifier:@"question"
		actions:@[answer] intentIdentifiers:@[] options:0]]];
	[c requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
		completionHandler:^(BOOL granted, NSError *err) { atAllowed = err != nil ? 2 : granted ? 1 : 0; }];
}

static void checkNotifications(void) {
	[[UNUserNotificationCenter currentNotificationCenter] getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *s) {
		if (atAllowed != 2) {
			atAllowed = s.authorizationStatus == UNAuthorizationStatusNotDetermined ? -1 :
				(s.authorizationStatus == UNAuthorizationStatusDenied ? 0 : 1);
		}
	}];
}

static int notificationsAllowed(void) { return atAllowed; }

static void postNotification(const char *qid, const char *title, const char *body, int question) {
	UNMutableNotificationContent *n = [UNMutableNotificationContent new];
	n.title = [NSString stringWithUTF8String:title];
	n.body = [NSString stringWithUTF8String:body];
	if (question) {
		n.categoryIdentifier = @"question";
	}
	n.sound = [UNNotificationSound defaultSound];
	[[UNUserNotificationCenter currentNotificationCenter] addNotificationRequest:[UNNotificationRequest
		requestWithIdentifier:[NSString stringWithUTF8String:qid] content:n trigger:nil] withCompletionHandler:nil];
}

static void withdrawNotification(const char *qid) {
	NSArray *ids = @[[NSString stringWithUTF8String:qid]];
	[[UNUserNotificationCenter currentNotificationCenter] removeDeliveredNotificationsWithIdentifiers:ids];
}
*/
import "C"

import (
	"os/exec"
	"unsafe"

	"rota/core"
)

// Notifications are macOS's own, from rota.app: each question is one, with a field to answer
// it right there; clicking it opens the answer dialog. Outside the app bundle macOS offers no
// notifications to a program, and when it refuses the app, osascript shows a plain one.

var bundled bool // the notification center is set up: the program runs as rota.app

func setupNotifications() {
	C.setupNotifications()
	bundled = true
}

// notificationsOff says macOS has notifications for rota turned off; it checks again as it goes.
func notificationsOff() bool {
	if !bundled {
		return false
	}
	C.checkNotifications()
	return C.notificationsAllowed() == 0
}

// notifyQuestion shows q as a notification; false while macOS does not allow rota's yet, so
// it is shown once they are.
func notifyQuestion(q core.Question) bool { return notify(q.ID, q.Agent+" asks you", q.Text, true) }

// notifyAnswer shows an agent's answer to the human's notes as a notification, under id; a
// click opens the pull request's review.
func notifyAnswer(id string, a core.Answer) bool {
	return notify(id, a.By+" answered you on "+a.Name, a.Text, false)
}

// notify shows a notification under id, with a field to answer when it is a question; false
// while macOS does not allow rota's yet.
func notify(id, title, body string, question bool) bool {
	switch {
	case !bundled || C.notificationsAllowed() == 2:
		go exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 1 of argv) with title (item 2 of argv)",
			"-e", "end run", body, title).Run()
	case C.notificationsAllowed() != 1:
		return false
	default:
		i, t, b := C.CString(id), C.CString(title), C.CString(body)
		defer C.free(unsafe.Pointer(i))
		defer C.free(unsafe.Pointer(t))
		defer C.free(unsafe.Pointer(b))
		q := C.int(0)
		if question {
			q = 1
		}
		C.postNotification(i, t, b, q)
	}
	return true
}

// withdraw takes the notification under id away: its question was answered, or its answer seen.
func withdraw(id string) {
	if bundled {
		i := C.CString(id)
		defer C.free(unsafe.Pointer(i))
		C.withdrawNotification(i)
	}
}
