package systests

import (
	"context"
	client "github.com/keybase/client/go/client"
	libkb "github.com/keybase/client/go/libkb"
	logger "github.com/keybase/client/go/logger"
	keybase1 "github.com/keybase/client/go/protocol/keybase1"
	service "github.com/keybase/client/go/service"
	clockwork "github.com/keybase/clockwork"
	rpc "github.com/keybase/go-framed-msgpack-rpc/rpc"
	"io"
	"testing"
	"time"
)

type smuUser struct {
	ctx            *smuContext
	devices        []*smuDeviceWrapper
	backupKeys     []backupKey
	usernamePrefix string
	username       string
}

type smuContext struct {
	t         *testing.T
	log       logger.Logger
	fakeClock clockwork.FakeClock
	users     map[string](*smuUser)
}

func newSMUContext(t *testing.T) *smuContext {
	ret := &smuContext{
		t:         t,
		users:     make(map[string](*smuUser)),
		fakeClock: clockwork.NewFakeClockAt(time.Now()),
	}
	return ret
}

func (t *smuContext) cleanup() {
	for _, v := range t.users {
		v.cleanup()
	}
}

func (u *smuUser) cleanup() {
	if u == nil {
		return
	}
	for _, d := range u.devices {
		d.tctx.Cleanup()
	}
}

// smuDeviceWrapper wraps a mock "device", meaning an independent running service and
// some connected clients.
type smuDeviceWrapper struct {
	ctx       *smuContext
	tctx      *libkb.TestContext
	clones    []*libkb.TestContext
	deviceKey keybase1.PublicKey
	stopCh    chan error
	service   *service.Service
	cli       rpc.GenericClient
	xp        rpc.Transporter
}

func (d *smuDeviceWrapper) KID() keybase1.KID {
	return d.deviceKey.KID
}

func (d *smuDeviceWrapper) startService(numClones int) {
	for i := 0; i < numClones; i++ {
		d.clones = append(d.clones, cloneContext(d.tctx))
	}
	d.stopCh = make(chan error)
	d.tctx.Tp.UpgradePerUserKey = true
	svc := service.NewService(d.tctx.G, false)
	d.service = svc
	startCh := svc.GetStartChannel()
	go func() {
		d.stopCh <- svc.Run()
	}()
	<-startCh
}

func (d *smuDeviceWrapper) stop() error {
	return <-d.stopCh
}

type smuTerminalUI struct{}

func (t smuTerminalUI) ErrorWriter() io.Writer                                        { return nil }
func (t smuTerminalUI) Output(string) error                                           { return nil }
func (t smuTerminalUI) OutputDesc(libkb.OutputDescriptor, string) error               { return nil }
func (t smuTerminalUI) OutputWriter() io.Writer                                       { return nil }
func (t smuTerminalUI) Printf(fmt string, args ...interface{}) (int, error)           { return 0, nil }
func (t smuTerminalUI) Prompt(libkb.PromptDescriptor, string) (string, error)         { return "", nil }
func (t smuTerminalUI) PromptForConfirmation(prompt string) error                     { return nil }
func (t smuTerminalUI) PromptPassword(libkb.PromptDescriptor, string) (string, error) { return "", nil }
func (t smuTerminalUI) PromptYesNo(libkb.PromptDescriptor, string, libkb.PromptDefault) (bool, error) {
	return false, nil
}
func (t smuTerminalUI) Tablify(headings []string, rowfunc func() []string) { return }
func (t smuTerminalUI) TerminalSize() (width int, height int)              { return }

func (d *smuDeviceWrapper) popClone() *libkb.TestContext {
	if len(d.clones) == 0 {
		panic("ran out of cloned environments")
	}
	ret := d.clones[0]
	d.clones = d.clones[1:]
	ui := genericUI{
		g:          ret.G,
		TerminalUI: smuTerminalUI{},
	}
	ret.G.SetUI(&ui)
	return ret
}
func (ctx *smuContext) setupDevice(u *smuUser) *smuDeviceWrapper {
	tctx := setupTest(ctx.t, u.usernamePrefix)
	tctx.G.SetClock(ctx.fakeClock)
	ret := &smuDeviceWrapper{ctx: ctx, tctx: tctx}
	u.devices = append(u.devices, ret)
	if ctx.log == nil {
		ctx.log = tctx.G.Log
	}
	return ret
}

func (ctx *smuContext) installKeybaseForUser(usernamePrefix string, numClones int) *smuUser {
	user := &smuUser{ctx: ctx, usernamePrefix: usernamePrefix}
	ctx.users[usernamePrefix] = user
	dev := ctx.setupDevice(user)
	dev.startService(numClones)
	dev.startClient()
	return user
}

func (u *smuUser) primaryDevice() *smuDeviceWrapper {
	return u.devices[0]
}

func (d *smuDeviceWrapper) userClient() keybase1.UserClient {
	return keybase1.UserClient{Cli: d.cli}
}

func (d *smuDeviceWrapper) rpcClient() rpc.GenericClient {
	return d.cli
}

func (d *smuDeviceWrapper) startClient() {
	var err error
	tctx := d.popClone()
	d.cli, d.xp, err = client.GetRPCClientWithContext(tctx.G)
	if err != nil {
		d.ctx.t.Fatal(err)
	}
}

func (d *smuDeviceWrapper) loadEncryptionKIDs() (devices []keybase1.KID, backups []backupKey) {
	keyMap := make(map[keybase1.KID]keybase1.PublicKey)
	keys, err := d.userClient().LoadMyPublicKeys(context.TODO(), 0)
	if err != nil {
		d.ctx.t.Fatalf("Failed to LoadMyPublicKeys: %s", err)
	}
	for _, key := range keys {
		keyMap[key.KID] = key
	}

	for _, key := range keys {
		if key.IsSibkey {
			continue
		}
		parent, found := keyMap[keybase1.KID(key.ParentID)]
		if !found {
			continue
		}

		switch parent.DeviceType {
		case libkb.DeviceTypePaper:
			backups = append(backups, backupKey{KID: key.KID, deviceID: parent.DeviceID})
		case libkb.DeviceTypeDesktop:
			devices = append(devices, key.KID)
		default:
		}
	}
	return devices, backups
}

func (u *smuUser) signup() {
	ctx := u.ctx
	userInfo := randomUser(u.usernamePrefix)
	dw := u.primaryDevice()
	tctx := dw.popClone()
	g := tctx.G
	signupUI := signupUI{
		info:         userInfo,
		Contextified: libkb.NewContextified(g),
	}
	g.SetUI(&signupUI)
	signup := client.NewCmdSignupRunner(g)
	signup.SetTest()
	if err := signup.Run(); err != nil {
		ctx.t.Fatal(err)
	}
	ctx.t.Logf("signed up %s", userInfo.username)
	u.username = userInfo.username
	var backupKey backupKey
	devices, backups := dw.loadEncryptionKIDs()
	if len(devices) != 1 {
		ctx.t.Fatalf("Expected 1 device back; got %d", len(devices))
	}
	if len(backups) != 1 {
		ctx.t.Fatalf("Expected 1 backup back; got %d", len(backups))
	}
	dw.deviceKey.KID = devices[0]
	backupKey = backups[0]
	backupKey.secret = signupUI.info.displayedPaperKey
	u.backupKeys = append(u.backupKeys, backupKey)
}

type smuTeam struct {
	name string
}

func (u *smuUser) getTeamsClient() keybase1.TeamsClient {
	return keybase1.TeamsClient{Cli: u.primaryDevice().rpcClient()}
}

func (u *smuUser) pollForMembershipUpdate(team smuTeam, kg keybase1.PerTeamKeyGeneration) keybase1.TeamDetails {
	wait := 10 * time.Millisecond
	var totalWait time.Duration
	i := 0
	for {
		cli := u.getTeamsClient()
		details, err := cli.TeamGet(context.TODO(), keybase1.TeamGetArg{Name: team.name, ForceRepoll: true})
		if err != nil {
			u.ctx.t.Fatal(err)
		}
		if details.KeyGeneration == kg {
			u.ctx.log.Debug("found key generation 2")
			return details
		}
		if i == 9 {
			break
		}
		i++
		u.ctx.log.Debug("in pollForMembershipUpdate: iter=%d; missed it, now waiting for %s", i, wait)
		time.Sleep(wait)
		totalWait += wait
		wait = wait * 2
	}
	u.ctx.t.Fatalf("Failed to find the needed key generation (%d) after %s of waiting (%d iterations)", kg, totalWait, i)
	return keybase1.TeamDetails{}
}

func (u *smuUser) createTeam(writers []*smuUser) smuTeam {
	name := u.username + "t"
	cli := u.getTeamsClient()
	err := cli.TeamCreate(context.TODO(), keybase1.TeamCreateArg{Name: name})
	if err != nil {
		u.ctx.t.Fatal(err)
	}
	for _, w := range writers {
		err = cli.TeamAddMember(context.TODO(), keybase1.TeamAddMemberArg{
			Name:     name,
			Username: w.username,
			Role:     keybase1.TeamRole_WRITER,
		})
		if err != nil {
			u.ctx.t.Fatal(err)
		}
	}
	return smuTeam{name: name}
}

func (u *smuUser) reset() {
	err := u.primaryDevice().userClient().ResetUser(context.TODO(), 0)
	if err != nil {
		u.ctx.t.Fatal(err)
	}
}

func (u *smuUser) getPrimaryGlobalContext() *libkb.GlobalContext {
	return u.primaryDevice().tctx.G
}
