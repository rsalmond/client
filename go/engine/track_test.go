package engine

import (
	"testing"

	"github.com/keybase/client/go/libkb"
	keybase1 "github.com/keybase/client/protocol/go"
)

func runTrack(tc libkb.TestContext, fu *FakeUser, username string) (idUI *FakeIdentifyUI, them *libkb.User, err error) {
	return runTrackWithOptions(tc, fu, username, TrackOptions{})
}

func runTrackWithOptions(tc libkb.TestContext, fu *FakeUser, username string, options TrackOptions) (idUI *FakeIdentifyUI, them *libkb.User, err error) {
	idUI = &FakeIdentifyUI{
		Fapr: keybase1.FinishAndPromptRes{
			TrackLocal:  options.TrackLocalOnly,
			TrackRemote: !options.TrackLocalOnly,
		},
	}

	arg := &TrackEngineArg{
		TheirName: username,
		Options:   options,
	}
	ctx := &Context{
		LogUI:      tc.G.UI.GetLogUI(),
		IdentifyUI: idUI,
		SecretUI:   fu.NewSecretUI(),
	}

	eng := NewTrackEngine(arg, tc.G)
	err = RunEngine(eng, ctx)
	them = eng.User()
	return
}

func assertTracked(t *testing.T, fu *FakeUser, theirName string) {
	me, err := libkb.LoadMe(libkb.LoadUserArg{})
	if err != nil {
		t.Fatal(err)
	}
	them, err := libkb.LoadUser(libkb.LoadUserArg{Name: theirName})
	if err != nil {
		t.Fatal(err)
	}
	s, err := me.GetTrackingStatementFor(them.GetName(), them.GetUID())
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected a tracking statement; but didn't see one")
	}
}

func trackAlice(tc libkb.TestContext, fu *FakeUser) {
	trackAliceWithOptions(tc, fu, TrackOptions{})
}

func trackAliceWithOptions(tc libkb.TestContext, fu *FakeUser, options TrackOptions) {
	idUI, res, err := runTrackWithOptions(tc, fu, "t_alice", options)
	if err != nil {
		tc.T.Fatal(err)
	}
	checkAliceProofs(tc.T, idUI, res)
	assertTracked(tc.T, fu, "t_alice")
	return
}

func trackBob(tc libkb.TestContext, fu *FakeUser) {
	trackBobWithOptions(tc, fu, TrackOptions{})
}

func trackBobWithOptions(tc libkb.TestContext, fu *FakeUser, options TrackOptions) {
	idUI, res, err := runTrackWithOptions(tc, fu, "t_bob", options)
	if err != nil {
		tc.T.Fatal(err)
	}
	checkBobProofs(tc.T, idUI, res)
	assertTracked(tc.T, fu, "t_bob")
	return
}

func TestTrack(t *testing.T) {
	tc := SetupEngineTest(t, "track")
	defer tc.Cleanup()
	fu := CreateAndSignupFakeUser(tc, "track")

	trackAlice(tc, fu)
	defer untrackAlice(tc, fu)

	// Assert that we gracefully handle the case of no login
	tc.G.Logout()
	_, _, err := runTrack(tc, fu, "t_bob")
	if err == nil {
		t.Fatal("expected logout error; got no error")
	} else if _, ok := err.(libkb.LoginRequiredError); !ok {
		t.Fatalf("expected a LoginRequireError; got %s", err.Error())
	}
	fu.LoginOrBust(tc)
	trackBob(tc, fu)
	defer untrackBob(tc, fu)

	// try tracking a user with no keys
	_, _, err = runTrack(tc, fu, "t_ellen")
	if err == nil {
		t.Errorf("expected error tracking t_ellen, got nil")
	}
	return
}

// tests tracking a user that doesn't have a public key (#386)
func TestTrackNoPubKey(t *testing.T) {
	tc := SetupEngineTest(t, "track")
	defer tc.Cleanup()
	fu := CreateAndSignupFakeUser(tc, "track")
	tc.G.Logout()

	tracker := CreateAndSignupFakeUser(tc, "track")
	_, _, err := runTrack(tc, tracker, fu.Username)
	if err != nil {
		t.Fatalf("error tracking user w/ no pgp key: %s", err)
	}
}
