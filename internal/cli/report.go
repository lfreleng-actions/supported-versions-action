// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

func (e *env) matrix(ctx context.Context, g *globals, args []string) error {
	fs := newFlagSet("matrix", g)
	text := fs.String("constraint", "", "apply the constraint `EXPR`, in the tool's constraint syntax")
	project := fs.String("project", "", "detect the constraint in the project in `DIR`")
	strict := fs.Bool("strict", false, "fail when the constraint allows no line of the policy matrix")
	rest, err := e.parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("matrix takes one tool, got %d arguments", len(rest))
	}
	if isSet(fs, "constraint") && isSet(fs, "project") {
		return usagef("matrix: --constraint and --project are mutually exclusive")
	}
	if err = g.validate(); err != nil {
		return err
	}
	req := versions.MatrixRequest{Constraint: *text, ProjectDir: *project, Strict: *strict}
	return e.resolve(g, func(r *versions.Resolver) (versions.Report, error) {
		return r.Matrix(ctx, rest[0], req)
	})
}

func (e *env) check(ctx context.Context, g *globals, args []string) error {
	fs := newFlagSet("check", g)
	fail := fs.Bool("fail", false, "exit 3 when the version is not supported")
	rest, err := e.parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return usagef("check takes a tool and a version, got %d arguments", len(rest))
	}
	if err = g.validate(); err != nil {
		return err
	}
	var rep versions.Report
	err = e.resolve(g, func(r *versions.Resolver) (versions.Report, error) {
		var checkErr error
		rep, checkErr = r.Check(ctx, rest[0], rest[1])
		return rep, checkErr
	})
	if err != nil {
		return err
	}
	if *fail && rep.Check != nil && !rep.Check.Supported {
		return fmt.Errorf("%s %s is %w (status %s)", rep.Tool, rep.Check.Version, errNotSupported, rep.Check.Status)
	}
	return nil
}

func (e *env) list(ctx context.Context, g *globals, args []string) error {
	fs := newFlagSet("list", g)
	rest, err := e.parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("list takes at most one tool, got %d arguments", len(rest))
	}
	if err = g.validate(); err != nil {
		return err
	}
	if len(rest) == 1 {
		return e.resolve(g, func(r *versions.Resolver) (versions.Report, error) {
			return r.Matrix(ctx, rest[0], versions.MatrixRequest{})
		})
	}
	cat, err := catalog.Embedded()
	if err != nil {
		return err
	}
	return e.listCatalog(g, cat)
}

func isSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// resolve builds a resolver, runs fn and prints the report fn returns,
// even when fn returns an error with it.
func (e *env) resolve(g *globals, fn func(*versions.Resolver) (versions.Report, error)) (err error) {
	opts := versions.Options{
		Fetch:            g.fetchOptions(),
		Now:              e.hooks.now,
		Snapshot:         e.hooks.snapshot,
		EndOfLifeBaseURL: e.hooks.endOfLifeBaseURL,
		ManifestBaseURL:  e.hooks.manifestBaseURL,
	}
	if g.policy != "" {
		if opts.Overlay, err = catalog.LoadOverlay(g.policy); err != nil {
			return err
		}
		opts.OverlayOrigin = g.policy
	}
	if !g.noCache {
		c, cacheErr := openCache(g.cacheDir)
		if cacheErr != nil {
			return fmt.Errorf("cache: %w", cacheErr)
		}
		defer func() { err = errors.Join(err, c.Close()) }()
		opts.Fetch.Cache = c
	}
	r, err := versions.New(opts)
	if err != nil {
		return err
	}
	rep, runErr := fn(r)
	if rep.Tool == "" {
		return runErr
	}
	return errors.Join(e.emit(g, rep), runErr)
}

func openCache(dir string) (*cache.Cache, error) {
	if dir == "" {
		return cache.Open()
	}
	return cache.New(dir)
}

// emit prints rep in the chosen format.
func (e *env) emit(g *globals, rep versions.Report) error {
	if g.verbose {
		e.logSources(rep)
	}
	if g.format == formatJSON {
		data, err := rep.JSON()
		if err != nil {
			return err
		}
		_, err = e.stdout.Write(data)
		return err
	}
	if err := e.writeWarnings(rep); err != nil {
		return err
	}
	return writeText(e.stdout, rep)
}

func (e *env) logSources(rep versions.Report) {
	p := &printer{w: e.stderr}
	for _, s := range rep.Source.Sources {
		fetched := "never fetched"
		if s.FetchedAt != nil {
			fetched = "fetched " + s.FetchedAt.UTC().Format(time.RFC3339)
		}
		p.printf("source: %s %s: %s, %s\n", s.Type, s.Ref, s.Kind, fetched)
	}
}

func (e *env) writeWarnings(rep versions.Report) error {
	p := &printer{w: e.stderr}
	for _, w := range rep.Warnings {
		p.printf("warning: %s\n", w)
	}
	return p.err
}

// writeText prints the human-readable form of rep.
func writeText(w io.Writer, rep versions.Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	p := &printer{w: tw}
	p.printf("tool:\t%s (%s)\n", rep.Tool, rep.Name)
	p.printf("matrix:\t%s\n", dash(strings.Join(rep.Matrix.Versions, " ")))
	p.printf("latest:\t%s\n", dash(rep.Latest))
	p.printf("source:\t%s\n", sourceText(rep.Source))
	if c := rep.Constraint; c != nil {
		origin := ""
		if c.Origin != "" {
			origin = ", from " + c.Origin
		}
		p.printf("constraint:\t%s (%s%s)\n", c.Text, c.Outcome, origin)
	}
	if c := rep.Check; c != nil {
		p.printf("\ncheck:\t%s\n", c.Version)
		p.printf("line:\t%s\n", c.Line)
		p.printf("status:\t%s\n", c.Status)
		p.printf("supported:\t%s\n", yesNo(c.Supported))
		p.printf("policy excluded:\t%s\n", yesNo(c.PolicyExcluded))
		p.printf("in matrix:\t%s\n", yesNo(c.InMatrix))
		p.printf("reason:\t%s\n", dash(c.Reason))
		p.printf("eol:\t%s\n", eolText(c.EOL, c.DaysToEOL))
	} else {
		p.printf("\nLINE\tSTATUS\tMATRIX\tREASON\tNEWEST\tEOL\n")
		for _, l := range rep.Lines {
			p.printf("%s\t%s\t%s\t%s\t%s\t%s\n", l.Line, l.Status, yesNo(l.InMatrix), dash(l.Reason), dash(l.Newest), dash(l.EOL))
		}
	}
	if p.err != nil {
		return p.err
	}
	return tw.Flush()
}

func sourceText(s versions.SourceSummary) string {
	if s.SnapshotDate == "" {
		return string(s.Kind)
	}
	if s.Kind == versions.KindSnapshot {
		return "snapshot of " + s.SnapshotDate
	}
	return fmt.Sprintf("%s (with the snapshot of %s)", s.Kind, s.SnapshotDate)
}

func eolText(day string, days *int) string {
	if day == "" {
		return "-"
	}
	if days == nil {
		return day
	}
	return fmt.Sprintf("%s (%d days)", day, *days)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// listRow is one tool in the catalog listing.
type listRow struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Aliases       []string `json:"aliases"`
	VersionScheme string   `json:"version_scheme"`
	MatrixKey     string   `json:"matrix_key"`
	Sources       []string `json:"sources"`
}

func catalogRows(cat *catalog.Catalog) []listRow {
	rows := make([]listRow, 0, len(cat.Tools))
	for _, id := range cat.IDs() {
		t := cat.Tools[id]
		row := listRow{
			ID:            id,
			Name:          t.Name,
			Aliases:       append([]string{}, t.Aliases...),
			VersionScheme: t.VersionScheme,
			MatrixKey:     t.MatrixKey,
			Sources:       make([]string, 0, len(t.Sources)),
		}
		for _, s := range t.Sources {
			row.Sources = append(row.Sources, s.Type)
		}
		rows = append(rows, row)
	}
	return rows
}

func (e *env) listCatalog(g *globals, cat *catalog.Catalog) error {
	rows := catalogRows(cat)
	if g.format == formatJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	p := &printer{w: tw}
	p.printf("ID\tNAME\tALIASES\tSCHEME\tMATRIX KEY\tSOURCES\n")
	for _, r := range rows {
		p.printf("%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, dash(strings.Join(r.Aliases, ",")),
			r.VersionScheme, r.MatrixKey, strings.Join(r.Sources, ","))
	}
	if p.err != nil {
		return p.err
	}
	return tw.Flush()
}
