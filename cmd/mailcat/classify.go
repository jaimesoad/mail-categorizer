package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/source"
)

func cmdClassify(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	interactive := fs.Bool("i", false, "modo interactivo: responde las preguntas y confirma")
	asJSON := fs.Bool("json", false, "una línea JSON por correo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("indica al menos un archivo .eml o directorio")
	}
	rd := bufio.NewReader(e.in)
	enc := json.NewEncoder(e.out)
	enc.SetEscapeHTML(false)
	for _, path := range fs.Args() {
		src, err := source.NewEML(path)
		if err != nil {
			return err
		}
		err = source.Each(ctx, src, func(m email.Email) error {
			out, err := eng.Process(ctx, m)
			if err != nil {
				return err
			}
			if *interactive {
				return converse(ctx, e, eng, rd, m, out)
			}
			if *asJSON {
				return enc.Encode(out)
			}
			printLine(e.out, m, out)
			return nil
		})
		if errors.Is(err, io.EOF) { // the user quit (q) or stdin ended
			fmt.Fprintln(e.out, "\nSesión terminada.")
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func printLine(w io.Writer, m email.Email, o *engine.Outcome) {
	state := map[string]string{
		engine.DecisionKnow: "SEGURO", engine.DecisionPredict: "PREDICCIÓN", engine.DecisionAsk: "PREGUNTA",
		engine.DecisionUnknown: "DESCONOCIDO", engine.DecisionUser: "USUARIO",
	}[o.Decision]
	fmt.Fprintf(w, "%-11s %-28s %5.0f%%  %-12s %s  «%s»", state, o.Path, o.Score*100, o.Method, o.EmailID, trunc(m.Subject, 50))
	if o.AlreadyProcessed {
		fmt.Fprint(w, " (ya procesado)")
	}
	if o.NeedsQuestion && o.Question != nil {
		fmt.Fprintf(w, "\n            ↳ pregunta pendiente: %s", o.Question.Text)
	}
	fmt.Fprintln(w)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func prompt(rd *bufio.Reader, w io.Writer, msg string) (string, error) {
	fmt.Fprint(w, msg)
	line, err := rd.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// converse runs the Akinator loop for one email on the terminal.
func converse(ctx context.Context, e env, eng *engine.Engine, rd *bufio.Reader, m email.Email, out *engine.Outcome) error {
	w := e.out
	fmt.Fprintf(w, "\n━━ %s\n   De: %s\n   Asunto: %s\n", out.EmailID, m.From.Email, trunc(m.Subject, 80))
	if out.AlreadyProcessed {
		fmt.Fprintln(w, "   (ya procesado antes)")
	}
	for out.NeedsQuestion && out.Question != nil {
		q := out.Question
		fmt.Fprintf(w, "\n? %s\n", q.Text)
		hint := "[s]í / [n]o"
		if q.Type != "boolean" {
			var opts []string
			for _, a := range q.Answers {
				opts = append(opts, fmt.Sprintf("%s=%s", a.ID, a.Label))
			}
			hint = strings.Join(opts, " | ")
		}
		line, err := prompt(rd, w, fmt.Sprintf("  %s, [o]mitir, [c]ategoría manual, [q] salir > ", hint))
		if err != nil {
			return err
		}
		switch strings.ToLower(line) {
		case "q", "quit":
			return io.EOF
		case "o", "omitir", "skip", "":
			out, err = eng.Skip(ctx, out.EmailID, q.ID)
		case "c":
			cat, cerr := chooseCategory(e, eng, rd)
			if cerr != nil {
				return cerr
			}
			if cat == "" {
				continue
			}
			out, err = eng.Move(ctx, out.EmailID, cat)
		default:
			ans := line
			if q.Type == "boolean" {
				ans = map[string]string{"s": "yes", "si": "yes", "sí": "yes"}[strings.ToLower(line)]
				if ans == "" {
					ans = line
				}
			}
			out, err = eng.Answer(ctx, out.EmailID, q.ID, ans)
		}
		if err != nil {
			if errors.Is(err, engine.ErrInvalid) {
				fmt.Fprintln(w, "  ✗", err)
				continue
			}
			return err
		}
	}
	return finishInteractive(ctx, e, eng, rd, out)
}

func finishInteractive(ctx context.Context, e env, eng *engine.Engine, rd *bufio.Reader, out *engine.Outcome) error {
	w := e.out
	fmt.Fprintln(w)
	for _, l := range out.Explanation.Lines() {
		fmt.Fprintln(w, "  "+l)
	}
	if out.Learned {
		fmt.Fprintln(w, "  ✓ Aprendido: la próxima vez preguntaré menos.")
	}
	switch {
	case out.Decision == engine.DecisionUnknown:
		fmt.Fprintln(w, "\n  No sé dónde va. Elige una categoría (o Enter para dejarlo sin clasificar):")
		cat, err := chooseCategory(e, eng, rd)
		if err != nil || cat == "" {
			return err
		}
		o, err := eng.Move(ctx, out.EmailID, cat)
		if err == nil {
			fmt.Fprintf(w, "  → %s (aprendido)\n", o.Path)
		}
		return err
	case out.RequestConfirmation:
		line, err := prompt(rd, w, fmt.Sprintf("\n  ¿Es correcto «%s»? [S/n/categoría] > ", out.Path))
		if err != nil {
			return err
		}
		switch strings.ToLower(line) {
		case "", "s", "si", "sí", "y", "yes":
			_, err = eng.Feedback(ctx, out.EmailID, true, "")
		case "n", "no":
			cat, cerr := chooseCategory(e, eng, rd)
			if cerr != nil || cat == "" {
				return cerr
			}
			_, err = eng.Feedback(ctx, out.EmailID, false, cat)
		default:
			_, err = eng.Feedback(ctx, out.EmailID, false, line)
		}
		if err != nil && errors.Is(err, engine.ErrInvalid) {
			fmt.Fprintln(w, "  ✗", err)
			return nil
		}
		return err
	}
	return nil
}

// chooseCategory lists the enabled categories and returns the chosen id ("" = none).
func chooseCategory(e env, eng *engine.Engine, rd *bufio.Reader) (string, error) {
	var ids []string
	eng.Tree().Walk(func(c categories.Category, depth int) {
		if c.ID == categories.UnknownID || !eng.Tree().Enabled(c.ID) {
			return
		}
		ids = append(ids, c.ID)
		fmt.Fprintf(e.out, "   %2d. %s%s  (%s)\n", len(ids), strings.Repeat("  ", depth), c.Name, c.ID)
	})
	line, err := prompt(rd, e.out, "   número o id > ")
	if err != nil || line == "" {
		return "", err
	}
	if n, perr := strconv.Atoi(line); perr == nil && n >= 1 && n <= len(ids) {
		return ids[n-1], nil
	}
	sort.Strings(ids)
	for _, id := range ids {
		if strings.EqualFold(id, line) {
			return id, nil
		}
	}
	fmt.Fprintln(e.out, "   ✗ categoría no válida")
	return "", nil
}
