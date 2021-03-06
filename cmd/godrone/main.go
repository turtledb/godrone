// Command godrone implements the GoDrone firmware.
package main

import (
	"flag"
	"github.com/felixge/godrone/attitude"
	"github.com/felixge/godrone/control"
	"github.com/felixge/godrone/drivers/motorboard"
	"github.com/felixge/godrone/drivers/navboard"
	"github.com/felixge/godrone/http"
	"github.com/felixge/log"
	gohttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	c = flag.String("c", "", "Absolute or relative path to config file.")

	// Convenience values to set the colors of all leds
	green  = motorboard.Leds(motorboard.LedGreen)
	orange = motorboard.Leds(motorboard.LedOrange)
	red    = motorboard.Leds(motorboard.LedRed)
)

func main() {
	flag.Parse()

	config := DefaultConfig
	if *c != "" {
		if err := LoadConfig(*c, &config); err != nil {
			panic(err)
		}
	}
	g, err := NewGoDrone(config)
	if err != nil {
		panic(err)
	}

	if err := g.Run(); err != nil {
		panic(err)
	}
}

// NewGoDrone returns a new GoDrone instance, or an error if it could not be
// created.
func NewGoDrone(c Config) (g GoDrone, err error) {
	g.log = log.DefaultLogger
	g.navboard = navboard.NewNavboard(c.NavboardTTY, g.log)
	g.motorboard, err = motorboard.NewMotorboard(c.MotorboardTTY)
	if err != nil {
		return
	}
	g.attitude = attitude.NewComplementary()
	g.control = control.NewControl(c.RollPID, c.PitchPID, c.YawPID)
	g.http = http.NewHandler(http.Config{
		Control: g.control,
		Log:     g.log,
	})
	g.httpAddr = c.HttpAddr
	g.navCh = make(chan navboard.Data)
	return
}

// GoDrone wraps the firmware state.
type GoDrone struct {
	log        *log.Logger
	navboard   *navboard.Navboard
	motorboard *motorboard.Motorboard
	attitude   *attitude.Complementary
	control    *control.Control
	http       *http.Handler
	httpAddr   string
	navCh      chan navboard.Data
}

// Run runs the firmware until SIGINT is received, or something goes terribly
// wrong.
func (g *GoDrone) Run() error {
	g.log.Info("Starting godrone")
	defer g.motorboard.Close()

	g.motorboard.SetLeds(green)
	time.Sleep(500 * time.Millisecond)
	g.motorboard.SetLeds(red)

	g.log.Info("Calibrating sensors")
	for {
		if err := g.navboard.Calibrate(); err == nil {
			break
		}
	}
	g.motorboard.SetLeds(green)

	go g.navboardLoop()
	go g.serveHttp()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)

	g.log.Info("Entering main loop")
mainLoop:
	for {
		select {
		case navData := <-g.navCh:
			attitudeData := g.attitude.Update(navData.Data)
			motorSpeeds := g.control.Update(attitudeData)
			if err := g.motorboard.SetSpeeds(motorSpeeds); err != nil {
				g.log.Error("Could not set motor speeds. err=%s", err)
			}
			g.http.Update(navData, attitudeData)
		case sig := <-sigCh:
			g.log.Info("Received signal=%s, shutting down", sig)
			break mainLoop
		}
	}
	return nil
}

func (g *GoDrone) navboardLoop() {
	g.log.Debug("Entering navboard loop")
	defer g.log.Debug("Leaving navboard loop")

	for {
		navData, err := g.navboard.NextData()
		if err != nil {
			continue
		}
		select {
		case g.navCh <- navData:
		default:
		}
	}
}

func (g *GoDrone) serveHttp() {
	g.log.Debug("Entering http loop")
	defer g.log.Debug("Leaving http loop")

	if err := gohttp.ListenAndServe(g.httpAddr, g.http); err != nil {
		g.log.Error("Failed to ListenAndServe. err=%s", err)
	}
}
