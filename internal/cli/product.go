package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/store"
)

func init() {
	commands = append(commands, command{"product",
		`product [--name TEXT] [--service NAME=DESCRIPTION]... [--topic NAME=KEYWORD,KEYWORD]... [--kind NAME=ROLE]...`,
		"show or change the product, its services, topics and decision kinds (publishers; an empty value removes an entry)", cmdProduct})
}

// pairs collects repeated NAME=VALUE flags.
type pairs map[string]string

func (p pairs) String() string { return "" }
func (p pairs) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("%q: want NAME=VALUE (an empty VALUE removes NAME)", v)
	}
	p[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	return nil
}

func cmdProduct(a *app, args []string) error {
	fs := a.flags("product")
	name := fs.String("name", "", "one line: what the team builds")
	services, topics, kinds := pairs{}, pairs{}, pairs{}
	fs.Var(services, "service", "NAME=DESCRIPTION of a service (repository name as in .dumen); repeatable")
	fs.Var(topics, "topic", "NAME=KEYWORD,KEYWORD: words that bring the topic's decisions to an agent's attention; repeatable")
	fs.Var(kinds, "kind", "NAME=ROLE: a kind of decision and the role that approves it; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	set := false
	fs.Visit(func(*flag.Flag) { set = true })
	if !set {
		return locked(a, p, func(root string) error {
			cfg, err := store.ReadConfig(root)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "product   %s\n", orDash(cfg.Product))
			for _, k := range model.SortedKeys(cfg.Services) {
				fmt.Fprintf(a.out, "service   %-16s %s\n", k, cfg.Services[k])
			}
			for _, k := range model.SortedKeys(cfg.Topics) {
				fmt.Fprintf(a.out, "topic     %-16s %s\n", k, strings.Join(cfg.Topics[k], ", "))
			}
			kk := cfg.KindsOrDefault()
			for _, k := range model.SortedKeys(kk) {
				who := "any team member"
				if r := kk[k].OwnerRole; r != "" {
					who = "role " + r
				}
				fmt.Fprintf(a.out, "kind      %-16s approved by %s\n", k, who)
			}
			return nil
		})
	}
	o := &op.SetProduct{Services: services, Topics: map[string][]string{}, Kinds: kinds}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "name" {
			o.Product = name
		}
	})
	for k, v := range topics {
		o.Topics[k] = splitList(v)
		if v == "" {
			o.Topics[k] = nil
		}
	}
	if _, err := a.submit(p, o); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Updated dumen.yml. Run `dumen product` to see it.")
	return nil
}
