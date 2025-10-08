package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"
	"github.com/qaisjp/go-discord-irc/bridge"
	log "github.com/sirupsen/logrus"
)

func main() {
	configPath := flag.String("config", "", "Config file to read configuration stuff from")
	debugMode := flag.Bool("debug", false, "Debug mode?")
	debugPresence := flag.Bool("debug-presence", false, "Include presence in debug output")
	devMode := flag.Bool("dev", false, "")

	flag.Parse()
	bridge.DevMode = *devMode

	if *configPath == "" {
		log.Fatalln("--config argument is required!")
		return
	}

	configFile, err := os.Open(*configPath)
	if err != nil {
		log.Fatalln(errors.Wrap(err, "could not read config"))
	}

	SetLogDebug(*debugMode)

	dibConfig := bridge.MakeDefaultConfig()
	dibConfig.DebugPresence = *debugPresence // Default value, if unspecified in the config
	err = bridge.LoadConfigInto(dibConfig, configFile)
	if err != nil {
		log.Fatalln(errors.Wrap(err, "could not read config"))
	}

	dib, err := bridge.New(dibConfig)
	if err != nil {
		log.WithField("error", err).Fatalln("Go-Discord-IRC failed to initialise.")
		return
	}

	log.Infoln("Cooldown duration for IRC puppets is", dib.Config.CooldownDuration)

	// Create new signal receiver
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)

	// Open the bot
	err = dib.Open()
	if err != nil {
		log.WithField("error", err).Fatalln("Go-Discord-IRC failed to start.")
		return
	}

	// Inform the user that things are happening!
	log.Infoln("Go-Discord-IRC is now running. Press Ctrl-C to exit.")

	// Watch for a shutdown signal
	<-sc

	log.Infoln("Shutting down Go-Discord-IRC...")

	// Cleanly close down the bridge.
	dib.Close()
}

func SetLogDebug(debug bool) {
	logger := log.StandardLogger()
	if debug {
		logger.SetLevel(log.DebugLevel)
	} else {
		logger.SetLevel(log.InfoLevel)
	}
}
