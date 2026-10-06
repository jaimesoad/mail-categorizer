// Command mailcat is the CLI and server of the Adaptive Email Categorizer.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/llm"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

const usage = `mailcat — clasificador de correo adaptativo (estilo Akinator)

Uso: mailcat [-config archivo.yaml] [-db archivo.db] <comando> [argumentos]

Comandos:
  init [-dir .]                          crea mailcat.yaml con la configuración de ejemplo
  classify [-i] [-json] <.eml|dir>...    clasifica correos (-i: modo interactivo con preguntas)
  feedback <id> -correct | -category X   confirma o corrige la clasificación de un correo
  move <id> <categoría>                  archiva manualmente ("Move to...")
  explain <id>                           ¿por qué quedó en esa categoría?
  categories                             muestra el árbol de categorías
  questions [list|approve <id>|reject <id>]   gestiona preguntas (p. ej. generadas por LLM)
  rules [-category X]                    muestra lo aprendido
  metrics [-json]                        métricas de calidad y esfuerzo
  eval -data <dir> [-json]               evaluación offline (dir/<categoría>/*.eml)
  pretrain (-mbox f | -eml dir) [...]    preentrena etiquetando un buzón existente con el LLM local
  audit [-n 20]                          verifica por muestreo lo que etiquetó el LLM
  export -out conocimiento.db            copia SIN contenido de correos (para llevarla a la nube)
  sample-data -out <dir> [-per 40]       genera un buzón sintético reproducible
  serve [-addr 127.0.0.1:8080]           API REST (+ bot de Telegram si está activado)
  health [-addr 127.0.0.1:8080]          comprueba que la API responde (healthcheck de Docker)
  purge (-id X | -all) [-delete]         borra el contenido de correos conservando lo aprendido
  backup <destino.db>                    copia consistente de la base de datos

Opciones globales (antes del comando):
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type env struct {
	in         io.Reader
	out, errw  io.Writer
	configPath string
	dbPath     string
	explicit   bool // -config was given on the command line (a missing file is then an error)
}

func run(args []string, in io.Reader, out, errw io.Writer) error {
	gfs := flag.NewFlagSet("mailcat", flag.ContinueOnError)
	gfs.SetOutput(errw)
	var e env
	e.in, e.out, e.errw = in, out, errw
	gfs.StringVar(&e.configPath, "config", os.Getenv("MAILCAT_CONFIG"), "archivo de configuración YAML (por defecto ./mailcat.yaml o la config embebida)")
	gfs.StringVar(&e.dbPath, "db", "", "ruta de la base SQLite (sobrescribe la config)")
	gfs.Usage = func() { fmt.Fprint(errw, usage); gfs.PrintDefaults() }
	if err := gfs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	gfs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			e.explicit = true
		}
	})
	rest := gfs.Args()
	if len(rest) == 0 {
		gfs.Usage()
		return fmt.Errorf("falta el comando")
	}
	cmd, cargs := rest[0], rest[1:]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "help", "-h", "--help":
		gfs.Usage()
		return nil
	case "init":
		return cmdInit(e, cargs)
	case "sample-data":
		return cmdSampleData(e, cargs)
	case "eval":
		return cmdEval(ctx, e, cargs)
	case "classify":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdClassify(ctx, e, eng, cargs) })
	case "feedback":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdFeedback(ctx, e, eng, cargs) })
	case "move":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdMove(ctx, e, eng, cargs) })
	case "explain":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdExplain(ctx, e, eng, cargs) })
	case "categories":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdCategories(e, eng) })
	case "questions":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdQuestions(ctx, e, eng, cargs) })
	case "rules":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdRules(ctx, e, eng, cargs) })
	case "metrics":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdMetrics(ctx, e, eng, cargs) })
	case "health":
		return cmdHealth(ctx, e, cargs)
	case "pretrain":
		return cmdPretrain(ctx, e, cargs)
	case "audit":
		return cmdAudit(ctx, e, cargs)
	case "export":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdExport(ctx, e, eng, cargs) })
	case "serve":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdServe(ctx, e, eng, cargs) })
	case "purge":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdPurge(ctx, e, eng, cargs) })
	case "backup":
		return withEngine(ctx, e, func(eng *engine.Engine) error { return cmdBackup(ctx, e, eng, cargs) })
	}
	gfs.Usage()
	return fmt.Errorf("comando desconocido %q", cmd)
}

// loadConfig resolves the configuration: -config, ./mailcat.yaml, or the embedded default.
func loadConfig(e env) (*config.Config, error) {
	path := e.configPath
	if path == "" {
		if _, err := os.Stat("mailcat.yaml"); err == nil {
			path = "mailcat.yaml"
		}
	} else if _, err := os.Stat(path); err != nil && !e.explicit {
		path = "" // MAILCAT_CONFIG points to a file that does not exist yet: use the embedded default
	}
	cfg, err := config.LoadOrDefault(path)
	if err != nil {
		return nil, err
	}
	if e.dbPath != "" {
		cfg.Database = e.dbPath
	}
	return cfg, nil
}

func withEngine(ctx context.Context, e env, fn func(*engine.Engine) error) error {
	return withEngineOpts(ctx, e, true, nil, fn)
}

// withEngineOpts builds the engine. llmHooks=false skips the per-email LLM
// intent/question helpers (pre-training calls the model itself, once per email).
// tweak may adjust the loaded configuration (CLI flag overrides).
func withEngineOpts(ctx context.Context, e env, llmHooks bool, tweak func(*config.Config), fn func(*engine.Engine) error) error {
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	if tweak != nil {
		tweak(cfg)
	}
	built, err := cfg.Build()
	if err != nil {
		return err
	}
	db, err := storage.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	var opts []engine.Option
	if cfg.LLM.Enabled && llmHooks {
		p, err := llm.FromConfig(cfg.LLM)
		if err != nil {
			return err
		}
		opts = append(opts,
			engine.WithEnricher(llm.IntentEnricher{P: p, SendBody: cfg.LLM.SendBody}),
			engine.WithProposer(llm.QuestionGenerator{P: p, Tree: built.Tree}))
	}
	eng, err := engine.New(ctx, db, built, opts...)
	if err != nil {
		return err
	}
	return fn(eng)
}
