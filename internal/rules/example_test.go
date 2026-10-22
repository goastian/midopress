package rules_test

import (
	"fmt"
	"net/netip"

	"go.yaml.in/yaml/v3"

	"github.com/goastian/midopress/internal/rules"
)

const exampleConfig = `
rules:
  - match: '~d (bank|banco|paypal|health)\.'
    action: passthrough_tls
  - match: '~u \.(mp4|webm|m3u8|ts)$ | ~t video/'
    action: passthrough
  - match: '(~d a\.example | ~d b\.example) & ~t image/'
    profile: images
    set:
      quality: 60
`

func Example() {
	var cfg struct {
		Rules []rules.Rule `yaml:"rules"`
	}
	if err := yaml.Unmarshal([]byte(exampleConfig), &cfg); err != nil {
		fmt.Println(err)
		return
	}
	rs, err := rules.Compile(cfg.Rules, rules.WithProfiles("images"))
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(rs.EvaluateHost("www.paypal.com:443", netip.Addr{}, netip.Addr{}).Action)

	d := rs.Evaluate(&rules.Context{
		URL:         "https://a.example/pic.png",
		Host:        "a.example",
		HasResponse: true,
		Status:      200,
		ContentType: "image/png",
	})
	fmt.Println(d.Action == rules.ActionNone, d.Profile, d.Set["quality"], d.Matched)

	_, err = rules.Compile([]rules.Rule{{Match: `~d (`, Profile: "images"}, {Match: `~u x`, Action: "passthrough_tls"}})
	fmt.Println(err)
	// Output:
	// passthrough_tls
	// true images 60 [2]
	// rules[0].match: column 4: invalid regular expression for ~d: missing closing ): `(`
	// rules[1].match: passthrough_tls is decided at CONNECT time, so match may only use ~d, ~src, ~dst, ~all and ~none
}
