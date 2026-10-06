# Gmail + n8n + Telegram + LLM local: guía de puesta en marcha

Objetivo: que `mailcat` aprenda de **tu** buzón de Gmail, primero **preentrenado en tu máquina** con
tu Ollama (`gpt-oss:20b`) y después funcionando (en local o en la nube) con las preguntas por Telegram.

```
        FASE A — en tu máquina (una vez)                     FASE B — día a día (local o nube)
 Gmail ─(n8n wf 4)─► mailcat ◄── Ollama (etiqueta)       Gmail ─(n8n wf 1)─► mailcat ─► Telegram (preguntas)
                       │  mailcat audit (tú verificas)                        ▲    │ tus respuestas
                       ▼                                       n8n wf 2 ◄─────┘    ▼
                 mailcat export ─► conocimiento.db ───────►  (etiquetas mailcat/* en Gmail)
                 (sin asuntos ni cuerpos)
```

## 0. Requisitos (cosas que solo puedes hacer tú)

| Qué | Cómo |
|---|---|
| **Bot de Telegram** | En Telegram habla con `@BotFather` → `/newbot` → guarda el token. |
| **Credencial Gmail en n8n** | En n8n: *Credentials → Gmail OAuth2*. Necesita un cliente OAuth de Google Cloud (el asistente de n8n lo guía). Con la app en modo *testing* Google caduca el refresh token a los 7 días: publícala o reautoriza. Debe incluir el permiso `gmail.modify` (para etiquetar). |
| **Token de mailcat** | `openssl rand -hex 32`. En n8n: *Credentials → Header Auth*, nombre `mailcat token`, header `Authorization`, valor `Bearer <token>`. |
| **Ollama** | `ollama pull gpt-oss:20b` y que responda en `http://localhost:11434`. |

## 1. Arrancar mailcat junto a tu n8n (Docker)

```bash
cp .env.example .env            # rellena MAILCAT_TOKEN, TELEGRAM_BOT_TOKEN y N8N_NETWORK
docker ps --format '{{.Names}}\t{{.Networks}}'   # ¿en qué red está tu n8n? → N8N_NETWORK
mkdir -p data && export UID GID=$(id -g)
docker compose run --rm mailcat init -dir /data    # crea data/mailcat.yaml
```

Edita `data/mailcat.yaml`:
- **`categories`**: ajústalas a tu vida (las de ejemplo están en español). *Esto es lo más importante:*
  el LLM etiqueta con tus categorías; descríbelas con `description:` para que acierte más.
- `telegram.enabled: true` y, tras el siguiente paso, `telegram.chat_id`.
- `llm.base_url: http://host.docker.internal:11434/v1` si el contenedor debe hablar con Ollama del host
  (en Linux añade `extra_hosts: ["host.docker.internal:host-gateway"]` al servicio).

```bash
docker compose up -d --build
```

Escribe `/start` a tu bot: te responde con tu **chat id**. Ponlo en `telegram.chat_id` y
`docker compose restart mailcat`. (Hasta entonces el bot no envía nada, y solo obedece a ese chat.)

## 2. FASE A — preentrenar con tus correos antiguos

1. En n8n importa los 4 workflows de [`n8n/`](../n8n) (*Import from file*), asigna las credenciales
   (`Gmail account`, `mailcat token`) y **no actives aún** el 1 ni el 2.
2. Ejecuta a mano **3 · Crear etiquetas** (crea `mailcat/…` en Gmail).
3. Ejecuta a mano **4 · Importar histórico** (hasta 500 correos; sube `limit` o cambia el filtro `q` y repite).
   Se guardan **en silencio**: sin Telegram y sin tocar tu Gmail.
4. Etiqueta lo importado con tu LLM local (con mailcat corriendo en tu máquina, no en la nube):

   ```bash
   docker compose run --rm mailcat pretrain -from-db -base-url http://host.docker.internal:11434/v1 -model gpt-oss:20b
   ```
   Se puede pausar con Ctrl-C y reanudar. Mira el avance: el porcentaje *"coincide con lo que ya sabía"*
   debe subir mientras aprende.
5. **Verifica una muestra** (no te saltes esto, el LLM puede equivocarse de forma sistemática):

   ```bash
   docker compose run --rm -it mailcat audit -n 30      # o /auditar en Telegram, de uno en uno
   docker compose run --rm mailcat metrics              # "Fiabilidad del LLM según tus verificaciones"
   ```
   Tus correcciones **siempre** pesan más que las etiquetas del LLM y las desautorizan. Si la fiabilidad
   es baja, mejora las descripciones de las categorías y repite con otra muestra.
6. (Alternativa sin n8n) Con *Google Takeout* → `All Mail.mbox`: `mailcat pretrain -mbox archivo.mbox`.

## 3. FASE B — uso diario

- Activa **1 · Clasificar correos nuevos** (cada minuto) y **2 · Sincronizar etiquetas**.
- Si mailcat sabe, el correo recibe `mailcat/<categoría>` en Gmail. Si duda, te escribe por Telegram con
  botones (Sí/No/Omitir/Otra categoría). Tus respuestas y correcciones (también por Telegram) se aprenden y
  el workflow 2 corrige la etiqueta en Gmail.
- Con `telegram.llm_answers_first: true` el LLM local intenta responder primero (solo si mailcat corre
  donde llega a Ollama).

## 4. Llevarlo a la nube

El conocimiento aprendido viaja **sin el contenido de tus correos**:

```bash
docker compose run --rm mailcat export -out /data/conocimiento.db
# copia data/conocimiento.db como mailcat.db al volumen del servicio en la nube
```
`export` borra asuntos y cuerpos de la copia y conserva reglas aprendidas, estadísticas de preguntas y
clasificaciones (la similitud TF-IDF se reactiva con los correos nuevos que etiquetes). En la nube:

- Despliega la imagen (`Dockerfile`) con `MAILCAT_TOKEN`, `TELEGRAM_BOT_TOKEN` y un volumen persistente.
  **Ejecuta una sola instancia** (SQLite). Pon la API detrás de HTTPS si n8n está fuera de su red.
- Ollama no estará accesible desde la nube: ahí las dudas te llegan por Telegram.
- En los workflows cambia `http://mailcat:8080` por la URL de tu instancia.

## Qué ve cada servicio

- **Telegram** ve remitente y asunto de los correos por los que preguntas (`telegram.show_subject: false`
  lo reduce al dominio). Nunca el cuerpo.
- **Google/n8n** ven tu correo, como siempre. **mailcat** guarda asunto y cuerpo en tu SQLite
  (`privacy.store_body: false` los descarta; `mailcat purge -all` los borra conservando lo aprendido).
- **Ollama local**: el cuerpo se envía solo al endpoint que configures; si no es loopback se redacta y exige
  `llm.allow_external: true`.

## Lo que NO he podido probar

- Los workflows de n8n están escritos contra la documentación y el código fuente de n8n y sus scripts
  Code ejecutados con datos de ejemplo, **pero no importados en una instancia real**: si algún nodo marca
  un aviso al importarlo, ajusta ese campo.
- No he usado tu Gmail, tu bot de Telegram ni tu Ollama: están cubiertos con servidores simulados.
- `gpt-oss:20b` puede etiquetar mal categorías ambiguas: por eso existe la auditoría y el peso reducido de
  sus etiquetas (cuentan la mitad y, sin confirmación humana, no pasan de 0.70 de fuerza).
