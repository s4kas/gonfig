// Copyright (c) 2017 Steven Roose <steven@stevenroose.org>.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package gonfig

import (
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/spf13/pflag"
)

// Conf is used to specify the intended behavior of gonfig.
type Conf struct {
	// ConfigFileVariable is the config variable that will be read before looking
	// for a config file.  If no value is specified in the environment variables
	// of the command line flags, the default config file will be read.
	// This flag should be defined in the config file struct and referred to here
	// by its ID.  The default value for this variable is obviously ignored.
	ConfigFileVariable string

	// FileDisable disabled reading config variables from the config file.
	FileDisable bool
	// FileDefaultFilename is the default filename to look for for the config
	// file.  If this is empty and no filename is explicitly provided, parsing
	// a config file is skipped.
	FileDefaultFilename string
	// FileDecoder specifies the decoder function to be used for decoding the
	// config file.  The following decoders are provided, but the user can also
	// specify a custom decoder function:
	//  - DecoderYAML
	//  - DecoderTOML
	//  - DecoderJSON
	// If no decoder function is provided, gonfig tries to guess the function
	// based on the file extension and otherwise tries them all in the above
	// mentioned order.
	FileDecoder FileDecoderFn

	// FlagDisable disabled reading config variables from the command line flags.
	FlagDisable bool

	// EnvDisables disables reading config variables from the environment
	// variables.
	EnvDisable bool
	// EnvPrefix is the prefix to use for the the environment variables.
	// gonfig does not add an underscore after the prefix.
	EnvPrefix string

	// HelpDisable disables printing the help message when the --help or -h flag
	// is provided.
	HelpDisable bool
	// HelpMessage is the message printed before the list of the flags when the
	// user sets the --help flag.
	// The default is "Usage of [executable name]:".
	HelpMessage string
	// HelpDescription is the description to print for the help flag.
	// By default, this is "show this help menu".
	HelpDescription string
}

// setup is the struct that keeps track of the state of the program throughout
// the lifecycle of loading the configuration.
type setup struct {
	conf *Conf

	opts    []*option // Holds all top-level options in the config struct.
	allOpts []*option // Holds all options and all sub-options recursively.

	// Some cached variables to avoid having to generate them twice.
	configFilePath   string
	customConfigFile bool // Whether the config file is user-provided.
	flagSet          *pflag.FlagSet
}

// findCustomConfigFile finds out where to look for the config file.
// It looks in the environment variables and the command line flags.
// It returns an absolute path to the config file.
func findCustomConfigFile(s *setup) (string, error) {
	if s.conf.ConfigFileVariable == "" {
		return "", nil
	}

	// Check if the config struct defined a variable for the config file.
	var configOpt *option
	for _, opt := range s.opts {
		if opt.id == s.conf.ConfigFileVariable {
			configOpt = opt
			break
		}
	}
	if configOpt == nil {
		panic(fmt.Errorf("config variable name provided (%s), "+
			"but not defined in config struct", s.conf.ConfigFileVariable))
	}

	// Look if the user specified a config file.  We go in opposite priority
	// and return as soon as we find one.
	path, err := lookupConfigFileFlag(s, configOpt)
	if err != nil {
		return "", err
	}
	if path != "" {
		return filepath.Abs(path)
	}

	path, err = lookupConfigFileEnv(s, configOpt)
	if err != nil {
		return "", err
	}
	if path != "" {
		return filepath.Abs(path)
	}

	return "", nil
}

// setDefaults writes the default values in the field values if a default value
// has been provided.
func setDefaults(s *setup) error {
	for _, opt := range s.opts {
		if !opt.defaultSet {
			continue
		}

		opt.defaultValue = reflect.New(opt.value.Type()).Elem()
		if opt.isSlice {
			if err := parseSlice(opt.defaultValue, opt.defaul); err != nil {
				return fmt.Errorf(
					"error parsing default value for %s: %s", opt.fullID(), err)
			}
		} else {
			if err := parseSimpleValue(opt.defaultValue, opt.defaul); err != nil {
				return fmt.Errorf(
					"error parsing default value for %s: %s", opt.fullID(), err)
			}
		}

		if err := opt.setValue(opt.defaultValue); err != nil {
			return fmt.Errorf("error setting default value for %s: %s",
				opt.id, err)
		}
	}

	return nil
}

// Load loads the configuration of your program in the struct at c.
// Use conf to specify how gonfig should look for configuration variables.
//
// This method can panic if there was a problem in the configuration struct that
// is used (which should not happen at runtime), but will always try to produce
// an error instead if the user provided incorrect values.
//
// The recognised tags on the exported struct variables are:
//  - id: the keyword identifier (defaults to lowercase of variable name)
//  - default: the default value of the variable
//  - short: the shorthand used for command line flags (like -h)
//  - desc: the description of the config var, used in --help
func Load(c interface{}, conf Conf) error {
	s := &setup{
		conf: &conf,
	}

	if err := inspectConfigStructure(s, c); err != nil {
		panic(fmt.Errorf("error in config structure: %s", err))
	}

	if err := setDefaults(s); err != nil {
		panic(fmt.Errorf("error in default values: %s", err))
	}

	// Parse in order of opposite priority: file, env, flags

	if !s.conf.FileDisable {
		filename, err := findCustomConfigFile(s)
		if err != nil {
			return err
		}

		if filename != "" {
			s.customConfigFile = true
		} else {
			s.customConfigFile = false
			if s.conf.FileDefaultFilename != "" {
				filename, err = filepath.Abs(s.conf.FileDefaultFilename)
				if err != nil {
					return fmt.Errorf("failed to convert default config file "+
						"location to an absolute path: %s", err)
				}
			}
		}

		if filename != "" {
			s.configFilePath = filename
			if err := parseFile(s); err != nil {
				return err
			}
		}
	}

	if !s.conf.EnvDisable {
		if err := parseEnv(s); err != nil {
			return err
		}
	}

	if !s.conf.FlagDisable {
		if err := parseFlags(s); err != nil {
			return err
		}
	}

	return nil
}

// LoadRawFile loads the configuration of your program in the struct at c from
// the given raw config file contents.
// In this method, conf is only used to pass the FileDecoder option.
// Use conf to specify how gonfig should look for configuration variables.
//
// This method can panic if there was a problem in the configuration struct that
// is used (which should not happen at runtime), but will always try to produce
// an error instead if the user provided incorrect values.
//
// The recognised tags on the exported struct variables are:
//  - id: the keyword identifier (defaults to lowercase of variable name)
//  - default: the default value of the variable
//  - short: the shorthand used for command line flags (like -h)
//  - desc: the description of the config var, used in --help
func LoadRawFile(c interface{}, fileContent []byte, conf Conf) error {
	conf.EnvDisable = true
	conf.FlagDisable = true
	return LoadWithRawFile(c, fileContent, conf)
}

// LoadWithRawFile loads the configuration of your program in the struct at c
// by using the given contents for the config file.
// Use conf to specify how gonfig should look for configuration variables.
// As opposed to LoadRawFile, in this method, the other config sources are also
// loaded.
//
// This method can panic if there was a problem in the configuration struct that
// is used (which should not happen at runtime), but will always try to produce
// an error instead if the user provided incorrect values.
//
// The recognised tags on the exported struct variables are:
//  - id: the keyword identifier (defaults to lowercase of variable name)
//  - default: the default value of the variable
//  - short: the shorthand used for command line flags (like -h)
//  - desc: the description of the config var, used in --help
func LoadWithRawFile(c interface{}, fileContent []byte, conf Conf) error {
	s := &setup{
		conf: &conf,
	}

	if err := inspectConfigStructure(s, c); err != nil {
		panic(fmt.Errorf("error in config structure: %s", err))
	}

	if err := setDefaults(s); err != nil {
		panic(fmt.Errorf("error in default values: %s", err))
	}

	if s.conf.FileDisable {
		panic("can't use LoadWithRawFile with DisableFile set to true")
	}

	if err := parseFileContent(s, fileContent); err != nil {
		return err
	}

	if !s.conf.EnvDisable {
		if err := parseEnv(s); err != nil {
			return err
		}
	}

	if !s.conf.FlagDisable {
		if err := parseFlags(s); err != nil {
			return err
		}
	}

	return nil
}
