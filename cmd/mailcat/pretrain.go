package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/llm"
	"github.com/jaimesoad/mail-categorizer/internal/pretrain"
	"github.com/jaimesoad/mail-categorizer/internal/source"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

func cmdPretrain(ctx context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("pretrain", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	mbox := fs.String("mbox", "", "archivo .mbox (p. ej. 'Todo el correo.mbox' de Google Takeout)")
	eml := fs.String("eml", "", "directorio con archivos .eml")
	fromDB := fs.Bool("from-db", false, "etiqueta los correos ya guardados en la base (p. ej. importados por n8n)")
	limit := fs.Int("limit", 0, "máximo de correos a procesar (0 = todos)")
	skip := fs.Int("skip", 0, "ignora los primeros N correos")
	stride := fs.Int("stride", 1, "toma uno de cada K correos (muestreo)")
	sendBody := fs.Bool("send-body", true, "envía el cuerpo al LLM (recomendado solo con un LLM local)")
	model := fs.String("model", "", "modelo (sobrescribe llm.model)")
	baseURL := fs.String("base-url", "", "endpoint OpenAI-compatible (sobrescribe llm.base_url)")
	minConf := fs.Float64("min-confidence", 0.6, "confianza mínima del LLM para aceptar una etiqueta")
	if err := fs.Parse(args); err != nil {
		return err
	}
	n := 0
	for _, set := range []bool{*mbox != "", *eml != "", *fromDB} {
		if set {
			n++
		}
	}
	if n != 1 {
		return errors.New("indica exactamente uno de -mbox, -eml o -from-db")
	}
	tweak := func(c *config.Config) {
		c.LLM.Enabled = true
		if *model != "" {
			c.LLM.Model = *model
		}
		if *baseURL != "" {
			c.LLM.BaseURL = *baseURL
		}
	}
	return withEngineOpts(ctx, e, false, tweak, func(eng *engine.Engine) error {
		p, err := llm.FromConfig(eng.Config().LLM)
		if err != nil {
			return err
		}
		if !p.Local() {
			fmt.Fprintln(e.errw, "AVISO: el endpoint no es local; se redactarán correos, URLs y números antes de enviarlos.")
		}
		teacher := llm.Teacher{P: p, Tree: eng.Tree(), SendBody: *sendBody, Redact: !p.Local(), MinConfidence: *minConf}

		if *fromDB {
			fmt.Fprintf(e.out, "Etiquetando con %s los correos de la base que nadie ha etiquetado. Ctrl-C pausa; se puede reanudar.\n", eng.Config().LLM.Model)
			st, err := pretrain.RunDB(ctx, eng, teacher, pretrain.Options{Limit: *limit, ProgressEvery: 10, Progress: func(s pretrain.Stats) {
				fmt.Fprintf(e.out, "  %d correos · etiquetados %d · sin etiqueta %d · errores LLM %d · coincide con lo que ya sabía %.0f%%\n",
					s.Seen, s.Labeled, s.Unlabeled, s.LLMErrors, s.AgreementRate()*100)
			}})
			printPretrainSummary(e, st)
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(e.out, "\nPausado. Vuelve a ejecutar el mismo comando para continuar.")
				return nil
			}
			return err
		}
		var src source.Source
		var total int
		if *mbox != "" {
			m, err := source.NewMBox(*mbox)
			if err != nil {
				return err
			}
			src = m
		} else {
			d, err := source.NewEML(*eml)
			if err != nil {
				return err
			}
			total = d.Len()
			src = d
		}
		defer src.Close()

		fmt.Fprintf(e.out, "Preentrenando con %s (%s). Ctrl-C para pausar: se puede reanudar con el mismo comando.\n", eng.Config().LLM.Model, eng.Config().LLM.BaseURL)
		opts := pretrain.Options{Skip: *skip, Limit: *limit, Stride: *stride, ProgressEvery: 10,
			Progress: func(s pretrain.Stats) {
				of := ""
				if total > 0 {
					of = fmt.Sprintf("/%d", total)
				}
				fmt.Fprintf(e.out, "  %d%s correos · etiquetados %d · sin etiqueta %d · errores LLM %d · ya vistos %d · coincide con lo que ya sabía %.0f%%\n",
					s.Seen, of, s.Labeled, s.Unlabeled, s.LLMErrors, s.Duplicates, s.AgreementRate()*100)
			}}
		st, err := pretrain.Run(ctx, eng, teacher, src, opts)
		printPretrainSummary(e, st)
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(e.out, "\nPausado. Vuelve a ejecutar el mismo comando para continuar.")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, "\nSiguiente paso: `mailcat audit` para verificar una muestra de las etiquetas del LLM.")
		return nil
	})
}

func printPretrainSummary(e env, st pretrain.Stats) {
	fmt.Fprintf(e.out, "\nResumen: %d procesados, %d etiquetados, %d sin etiqueta (LLM dudoso), %d ya vistos, %d errores del LLM, %d mensajes ilegibles\n",
		st.Seen, st.Labeled, st.Unlabeled, st.Duplicates, st.LLMErrors, st.ParseErrors)
	cats := make([]string, 0, len(st.ByCategory))
	for c := range st.ByCategory {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool { return st.ByCategory[cats[i]] > st.ByCategory[cats[j]] })
	for _, c := range cats {
		fmt.Fprintf(e.out, "  %-24s %d\n", c, st.ByCategory[c])
	}
}

func cmdAudit(ctx context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	n := fs.Int("n", 20, "cuántas etiquetas del LLM revisar")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withEngineOpts(ctx, e, false, nil, func(eng *engine.Engine) error {
		labels, err := eng.DB().Labels(ctx)
		if err != nil {
			return err
		}
		sample := pretrain.AuditSample(labels, *n)
		if len(sample) == 0 {
			fmt.Fprintln(e.out, "No hay etiquetas del LLM pendientes de verificar.")
			return nil
		}
		rd := bufio.NewReader(e.in)
		fmt.Fprintf(e.out, "Revisa %d correos etiquetados por el LLM. Enter = correcto · número/id = otra categoría · s = saltar · q = salir\n", len(sample))
		right, wrong := 0, 0
	loop:
		for i, id := range sample {
			em, err := eng.DB().GetEmail(ctx, id)
			if err != nil {
				continue
			}
			cat := labels[id].Category
			body := strings.Join(strings.Fields(em.BodyText), " ")
			fmt.Fprintf(e.out, "\n[%d/%d] De: %s\n  Asunto: %s\n  %s\n  LLM → %s\n", i+1, len(sample), em.From.Email, trunc(em.Subject, 90), trunc(body, 240), eng.Tree().Path(cat))
			line, err := prompt(rd, e.out, "  ¿Correcto? > ")
			if err != nil {
				break
			}
			switch low := strings.ToLower(line); low {
			case "q":
				break loop
			case "s":
				continue
			case "", "y", "si", "sí":
				if _, err := eng.Feedback(ctx, id, true, ""); err != nil {
					return err
				}
				right++
			default:
				target := line
				switch {
				case low == "n" || low == "no":
					if target, err = chooseCategory(e, eng, rd); err != nil || target == "" {
						continue
					}
				case isNumber(line):
					if target, err = categoryByNumber(eng, line); err != nil {
						fmt.Fprintln(e.out, "  ✗", err)
						continue
					}
				}
				if _, err := eng.Feedback(ctx, id, false, target); err != nil {
					if errors.Is(err, engine.ErrInvalid) {
						fmt.Fprintln(e.out, "  ✗", err)
						continue
					}
					return err
				}
				wrong++
			}
		}
		if right+wrong > 0 {
			fmt.Fprintf(e.out, "\nEl LLM acertó %d de %d (%.0f%%). Tus correcciones ya pesan más que sus etiquetas.\n", right, right+wrong, 100*float64(right)/float64(right+wrong))
		}
		return nil
	})
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func categoryByNumber(eng *engine.Engine, s string) (string, error) {
	var ids []string
	eng.Tree().Walk(func(c categories.Category, _ int) {
		if c.ID != "unknown" && eng.Tree().Enabled(c.ID) {
			ids = append(ids, c.ID)
		}
	})
	var n int
	fmt.Sscanf(s, "%d", &n)
	if n < 1 || n > len(ids) {
		return "", fmt.Errorf("número fuera de rango (1-%d); escribe `n` para ver la lista", len(ids))
	}
	return ids[n-1], nil
}

func cmdExport(ctx context.Context, e env, eng *engine.Engine, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	out := fs.String("out", "", "archivo de destino")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out es obligatorio")
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s ya existe", *out)
	}
	if err := eng.DB().Backup(ctx, *out); err != nil {
		return err
	}
	cp, err := storage.Open(*out)
	if err != nil {
		return err
	}
	defer cp.Close()
	n, err := cp.PurgeAllContent(ctx, nowUTC())
	if err != nil {
		return err
	}
	if _, err := cp.ExecContext(ctx, `VACUUM`); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Conocimiento exportado a %s: reglas aprendidas, estadísticas de preguntas y clasificaciones.\nSe borró el asunto/cuerpo de %d correos (no salen de tu máquina). La similitud TF-IDF se reactivará sola con los correos nuevos que etiquetes.\n", *out, n)
	return nil
}
