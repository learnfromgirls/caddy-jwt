package jwt

import (
	"fmt"

	"github.com/mholt/caddy"
	"github.com/mholt/caddy/caddyhttp/httpserver"
	"github.com/learnfromgirls/safesecrets"
)

// RuleType distinguishes between ALLOW and DENY rules
type RuleType int

const (
	// ALLOW represents a rule that should allow access based on claim value
	ALLOW RuleType = iota

	// DENY represents a rule that should deny access based on claim value
	DENY
)

// EncryptionType specifies the valid configuration for a path
type EncryptionType int

const (
	// HS family of algorithms
	HMAC EncryptionType = iota + 1
	// RS and ES families of algorithms
	PKI
)

// Auth represents configuration information for the middleware
type Auth struct {
	Rules []Rule
	Next  httpserver.Handler
	Realm string
}

// Rule represents the configuration for a site
type Rule struct {
	Path          string
	ExceptedPaths []string
	AccessRules   []AccessRule
	Redirect      string
	AllowRoot     bool
	KeyBackends   []KeyBackend
	Passthrough   bool
	StripHeader   bool
}

// AccessRule represents a single ALLOW/DENY rule based on the value of a claim in
// a validated token
type AccessRule struct {
	Authorize RuleType
	Claim     string
	Value     string
}

var secretSetters []safesecrets.SecretSetter

func appendSS( ssa2... safesecrets.SecretSetter){
	for _, e := range ssa2 {
		secretSetters = append(secretSetters, e)
	}
}

func init() {
	caddy.RegisterPlugin("jwt", caddy.Plugin{
		ServerType: "http",
		Action:     Setup,
	})
	caddy.RegisterEventHook("setJWTSecret", setJWTSecretHook)
}


//type EventHook func(eventType EventName, eventInfo interface{}) error
func setJWTSecretHook(eventType caddy.EventName, eventInfo interface{}) error {
	//fmt.Printf("event hook called %v info=%v\n", eventType, eventInfo)
	if "setJWTSecret" == eventType {
		for _, e := range secretSetters {
			e.SetSecret(eventInfo.([] byte))
		}

	}
	return nil
}

// Setup is called by Caddy to parse the config block
func Setup(c *caddy.Controller) error {
	rules, err := parse(c)
	if err != nil {
		return err
	}

	c.OnStartup(func() error {
		fmt.Println("JWT middleware is initiated")
		return nil
	})

	host := httpserver.GetConfig(c).Addr.Host

	httpserver.GetConfig(c).AddMiddleware(func(next httpserver.Handler) httpserver.Handler {
		return &Auth{
			Rules: rules,
			Next:  next,
			Realm: host,
		}
	})

	return nil
}

func parse(c *caddy.Controller) ([]Rule, error) {
	defaultKeyBackends, err := NewDefaultKeyBackends()
	if err != nil {
		return nil, err
	}

	for _, ss := range defaultKeyBackends {
		appendSS( ss)
	}


	// This parses the following config blocks
	/*
		jwt /hello
		jwt /anotherpath
		jwt {
			path /hello
		}
	*/
	var rules []Rule
	for c.Next() {
		args := c.RemainingArgs()
		switch len(args) {
		case 0:
			// no argument passed, check the config block

			var r = Rule{
				KeyBackends: defaultKeyBackends,
			}
			for c.NextBlock() {
				switch c.Val() {
				case "path":
					if !c.NextArg() {
						// we are expecting a value
						return nil, c.ArgErr()
					}
					// return error if multiple paths in a block
					if len(r.Path) != 0 {
						return nil, c.ArgErr()
					}
					r.Path = c.Val()
					if c.NextArg() {
						// we are expecting only one value.
						return nil, c.ArgErr()
					}
				case "except":
					if !c.NextArg() {
						return nil, c.ArgErr()
					}
					r.ExceptedPaths = append(r.ExceptedPaths, c.Val())
					if c.NextArg() {
						// except only allows one path per declaration
						return nil, c.ArgErr()
					}
				case "allowroot":
					r.AllowRoot = true
				case "allow":
					args1 := c.RemainingArgs()
					if len(args1) != 2 {
						return nil, c.ArgErr()
					}
					r.AccessRules = append(r.AccessRules, AccessRule{Authorize: ALLOW, Claim: args1[0], Value: args1[1]})
				case "deny":
					args1 := c.RemainingArgs()
					if len(args1) != 2 {
						return nil, c.ArgErr()
					}
					r.AccessRules = append(r.AccessRules, AccessRule{Authorize: DENY, Claim: args1[0], Value: args1[1]})
				case "redirect":
					args1 := c.RemainingArgs()
					if len(args1) != 1 {
						return nil, c.ArgErr()
					}
					r.Redirect = args1[0]
				case "publickey":
					args1 := c.RemainingArgs()
					if len(args1) != 1 {
						return nil, c.ArgErr()
					}
					backend, err := NewLazyPublicKeyFileBackend(args1[0])
					if err != nil {
						return nil, c.Err(err.Error())
					}
					r.KeyBackends = append(r.KeyBackends, backend)
				case "secret":
					args1 := c.RemainingArgs()
					if len(args1) != 1 {
						return nil, c.ArgErr()
					}
					backend, err := NewLazyHmacKeyBackend(args1[0])
					if err != nil {
						return nil, c.Err(err.Error())
					}
					r.KeyBackends = append(r.KeyBackends, backend)
				case "passthrough":
					r.Passthrough = true
				case "strip_header":
					r.StripHeader = true
				}
			}
			rules = append(rules, r)
		case 1:
			rules = append(rules, Rule{
				Path:        args[0],
				KeyBackends: defaultKeyBackends,
			})
			// one argument passed
			if c.NextBlock() {
				// path specified, no block required.
				return nil, c.ArgErr()
			}
		default:
			// we want only one argument max
			return nil, c.ArgErr()
		}
	}

	// check all rules at least have a path and consistent encryption config
	for _, r := range rules {
		if r.Path == "" {
			return nil, fmt.Errorf("Each rule must have a path")
		}
		var encType EncryptionType
		for _, e := range r.KeyBackends {
			switch e.(type) {
			case *LazyHmacKeyBackend:
				if encType > 0 && encType != HMAC {
					return nil, fmt.Errorf("Configuration does not have a consistent encryption type for path %s.  Cannot use both HMAC and PKI for a single path value.", r.Path)
				}
				encType = HMAC
			case *LazyPublicKeyBackend:
				if encType > 0 && encType != PKI {
					return nil, fmt.Errorf("Configuration does not have a consistent encryption type for path %s.  Cannot use both HMAC and PKI for a single path value.", r.Path)
				}
				encType = PKI
			}
		}

	}

	return rules, nil
}
