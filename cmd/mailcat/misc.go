package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/api"
	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/eval"
	"github.com/jaimesoad/mail-categorizer/internal/llm"
	"github.com/jaimesoad/mail-categorizer/internal/metrics"
	"github.com/jaimesoad/mail-categorizer/internal/samples"
	"github.com/jaimesoad/mail-categorizer/internal/telegram"
	"log/slog"
	"net/http"
)

func cmdInit(e env, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	dir := fs.String("dir", ".", "directorio destino")
	force := fs.Bool("force", false, "sobrescribe si existe")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := filepath.Join(*dir, "mailcat.yaml")
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s ya existe (usa -force para sobrescribir)", path)
	}
	if err := os.WriteFile(path, config.DefaultYAML(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Configuración escrita en %s. Edita categorías, preguntas y reglas; luego prueba:\n  mailcat sample-data -out sample && mailcat classify -i sample/personal\n", path)
	return nil
}

func cmdSampleData(e env, args []string) error {
	fs := flag.NewFlagSet("sample-data", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	out := fs.String("out", "", "directorio de salida")
	per := fs.Int("per", 40, "correos por categoría")
	seed := fs.Int64("seed", 42, "semilla")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out es obligatorio")
	}
	n := 0
	for _, s := range samples.Generate(*seed, *per) {
		dir := filepath.Join(*out, s.Category)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, s.ID+".eml"), s.EML, 0o644); err != nil {
			return err
		}
		n++
	}
	fmt.Fprintf(e.out, "%d correos escritos en %s/<categoría>/*.eml\n", n, *out)
	return nil
}

func cmdEval(ctx context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	data := fs.String("data", "", "directorio con <categoría>/*.eml")
	asJSON := fs.Bool("json", false, "salida JSON")
	train := fs.Int("train", 60, "% entrenamiento")
	val := fs.Int("val", 20, "% validación (el resto es test)")
	window := fs.Int("window", 20, "tamaño de ventana de la curva de aprendizaje")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *data == "" {
		return errors.New("-data es obligatorio")
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	built, err := cfg.Build()
	if err != nil {
		return err
	}
	items, err := eval.LoadDir(*data)
	if err != nil {
		return err
	}
	for _, it := range items {
		if !built.Tree.Has(it.Expected) {
			return fmt.Errorf("el directorio %q no es una categoría de la configuración", it.Expected)
		}
	}
	res, err := eval.Run(ctx, built, items, eval.Options{TrainPct: *train, ValPct: *val, Window: *window})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(e.out).Encode(res)
	}
	fmt.Fprint(e.out, res.Text())
	return nil
}

func cmdFeedback(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	correct := fs.Bool("correct", false, "la clasificación es correcta")
	cat := fs.String("category", "", "categoría correcta")
	// allow `feedback <id> -flags`
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" || (!*correct && *cat == "") {
		return errors.New("uso: feedback <id> (-correct | -category X)")
	}
	out, err := eng.Feedback(ctx, id, *correct, *cat)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s → %s (aprendido: %v)\n", id, out.Path, out.Learned)
	return nil
}

func cmdMove(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	if len(args) != 2 {
		return errors.New("uso: move <id> <categoría>")
	}
	out, err := eng.Move(ctx, args[0], args[1])
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s → %s (aprendido: %v)\n", args[0], out.Path, out.Learned)
	return nil
}

func cmdExplain(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	if len(args) != 1 {
		return errors.New("uso: explain <id>")
	}
	x, err := eng.Explain(ctx, args[0])
	if err != nil {
		return err
	}
	for _, l := range x.Lines() {
		fmt.Fprintln(e.out, l)
	}
	return nil
}

func cmdCategories(e env, eng *engine.Engine) error {
	eng.Tree().Walk(func(c categories.Category, depth int) {
		off := ""
		if !c.Enabled {
			off = "  [desactivada]"
		}
		fmt.Fprintf(e.out, "%s%s  (%s)%s\n", strings.Repeat("  ", depth), c.Name, c.ID, off)
	})
	return nil
}

func cmdQuestions(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		qs, err := eng.DB().Questions(ctx)
		if err != nil {
			return err
		}
		for _, q := range qs {
			state := "activa"
			switch {
			case !q.Validated:
				state = "PENDIENTE de aprobación"
			case q.Disabled:
				state = "desactivada"
			}
			fmt.Fprintf(e.out, "%-22s [%s, %s] %s\n", q.ID, q.Source, state, q.Text)
		}
		return nil
	case "approve", "reject":
		if len(args) != 2 {
			return fmt.Errorf("uso: questions %s <id>", sub)
		}
		var err error
		if sub == "approve" {
			err = eng.ApproveQuestion(ctx, args[1])
		} else {
			err = eng.RejectQuestion(ctx, args[1])
		}
		if err == nil {
			fmt.Fprintf(e.out, "pregunta %s: %s\n", args[1], map[string]string{"approve": "aprobada", "reject": "rechazada"}[sub])
		}
		return err
	}
	return fmt.Errorf("subcomando desconocido %q", sub)
}

func cmdRules(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	cat := fs.String("category", "", "filtra por categoría")
	limit := fs.Int("n", 50, "máximo de reglas")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rs, err := eng.DB().ListRules(ctx, *cat, *limit)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		fmt.Fprintln(e.out, "Aún no se ha aprendido ninguna regla.")
	}
	for _, r := range rs {
		sign := "→"
		if r.Negative {
			sign = "≠"
		}
		fmt.Fprintf(e.out, "%-46s %s %-20s peso %.2f  (confirmaciones %d, rechazos %d)\n", r.Key, sign, eng.Tree().Path(r.CategoryID), r.Weight, r.Confirmations, r.Rejections)
	}
	return nil
}

func cmdMetrics(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	asJSON := fs.Bool("json", false, "salida JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rep, err := metrics.Compute(ctx, eng.DB())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Fprint(e.out, rep.Text())
	return nil
}

func cmdServe(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	addr := fs.String("addr", eng.Config().API.Addr, "dirección de escucha")
	if err := fs.Parse(args); err != nil {
		return err
	}
	token := ""
	if name := eng.Config().API.TokenEnv; name != "" {
		token = os.Getenv(name)
	}
	log := slog.New(slog.NewTextHandler(e.errw, nil))
	if token == "" && !isLoopbackAddr(*addr) {
		return fmt.Errorf("la API escucha en %s (no es loopback) sin token: define %s o usa 127.0.0.1", *addr, eng.Config().API.TokenEnv)
	}
	log.Info("API escuchando", "addr", *addr, "auth", token != "")

	tg := eng.Config().Telegram
	if !tg.Enabled {
		return api.New(eng, token, log).Serve(ctx, *addr)
	}
	botToken := os.Getenv(tg.TokenEnv)
	if botToken == "" {
		return fmt.Errorf("telegram.enabled pero la variable %s está vacía (el token se pide a @BotFather y nunca va en el YAML)", tg.TokenEnv)
	}
	opt := telegram.Options{ChatID: tg.ChatID, ShowSubject: tg.ShowSubject, MaxOpenPrompts: tg.MaxOpenPrompts, Log: log}
	if tg.LLMAnswers {
		cfg := eng.Config().LLM
		p, err := llm.FromConfig(cfg)
		if err != nil {
			return fmt.Errorf("telegram.llm_answers_first requiere un LLM válido: %w", err)
		}
		opt.Answerer = llm.Teacher{P: p, Tree: eng.Tree(), SendBody: cfg.SendBody, Redact: !p.Local()}
	}
	if tg.ChatID == 0 {
		log.Warn("telegram.chat_id es 0: escribe /start al bot para conocer tu chat id; hasta entonces no envía nada")
	}
	bot := telegram.New(&telegram.Client{Token: botToken}, eng, opt)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- bot.Run(runCtx) }()
	go func() { errc <- api.New(eng, token, log).Serve(runCtx, *addr) }()
	err := <-errc // if either side stops (port busy, bad token...), stop the other too
	cancel()
	<-errc
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func isLoopbackAddr(addr string) bool {
	return strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "localhost:") || strings.HasPrefix(addr, "[::1]")
}

func cmdPurge(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	id := fs.String("id", "", "correo a purgar")
	all := fs.Bool("all", false, "purga el contenido de todos los correos")
	del := fs.Bool("delete", false, "con -id: elimina el registro completo en vez de solo el contenido")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now().UTC()
	switch {
	case *all:
		n, err := eng.DB().PurgeAllContent(ctx, now)
		fmt.Fprintf(e.out, "contenido purgado en %d correos; las reglas aprendidas se conservan\n", n)
		return err
	case *id != "" && *del:
		return eng.DB().DeleteEmail(ctx, *id)
	case *id != "":
		return eng.DB().PurgeContent(ctx, *id, now)
	}
	return errors.New("uso: purge (-id X [-delete] | -all)")
}

func cmdBackup(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	if len(args) != 1 {
		return errors.New("uso: backup <destino.db>")
	}
	if _, err := os.Stat(args[0]); err == nil {
		return fmt.Errorf("%s ya existe", args[0])
	}
	if err := eng.DB().Backup(ctx, args[0]); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "copia escrita en %s\n", args[0])
	return nil
}

func nowUTC() time.Time { return time.Now().UTC() }

// cmdHealth is the container healthcheck (distroless images have no curl).
func cmdHealth(ctx context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	addr := fs.String("addr", "127.0.0.1:8080", "dirección de la API")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+*addr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz: HTTP %d", resp.StatusCode)
	}
	fmt.Fprintln(e.out, "ok")
	return nil
}
