# Especificación técnica — Adaptive Email Categorizer

## 1. Objetivo

Construir un sistema de clasificación inteligente de correos electrónicos inspirado en el principio de Akinator:

1. Recibir un correo.
2. Extraer características relevantes.
3. Determinar las categorías más probables.
4. Si la confianza es suficiente, clasificar automáticamente.
5. Si existe ambigüedad, seleccionar la pregunta que aporte mayor información y presentarla al usuario.
6. Incorporar la respuesta del usuario como conocimiento reutilizable.
7. Reducir progresivamente la necesidad de preguntar para correos futuros.

El sistema debe combinar reglas deterministas, clasificación probabilística, conocimiento histórico y un motor de preguntas adaptativas.

El diseño debe ser independiente del proveedor de correo. Gmail, Outlook, IMAP, archivos `.eml`, etc. deben tratarse como fuentes de entrada, no como parte esencial del motor.

---

# 2. Principios de diseño

## 2.1. Clasificar antes que preguntar

El sistema no debe convertir cada correo en un cuestionario.

Orden de prioridad:

1. Reglas de alta confianza.
2. Conocimiento previamente aprendido.
3. Clasificador.
4. Pregunta adaptativa únicamente cuando sea necesaria.

## 2.2. Preguntar únicamente cuando la respuesta sea útil

Una pregunta debe seleccionarse según cuánto reduzca la incertidumbre entre las categorías candidatas.

El sistema debe preferir preguntas que:

- separen muchas categorías;
- tengan respuestas fáciles de interpretar;
- tengan alta probabilidad de ser respondidas;
- no hayan sido preguntadas previamente para ese correo;
- sean compatibles con las características ya conocidas.

## 2.3. Separar extracción, clasificación y aprendizaje

No mezclar:

- parsing de correo;
- extracción de características;
- clasificación;
- selección de preguntas;
- persistencia;
- aprendizaje.

Cada componente debe poder probarse independientemente.

## 2.4. Explicabilidad

Toda clasificación debe poder responder:

> ¿Por qué este correo fue colocado aquí?

Ejemplo:

```text
Categoría: Finanzas / Banco

Razones:
- El remitente coincide con un dominio bancario conocido.
- Contiene una referencia a una transacción.
- Contiene un monto monetario.
- Correos similares fueron clasificados previamente como Banco.
```

---

# 3. Arquitectura

```text
                    ┌─────────────────────┐
                    │   Email Source      │
                    │ IMAP / API / .eml   │
                    └──────────┬──────────┘
                               │
                               ▼
                    ┌─────────────────────┐
                    │ Email Normalizer    │
                    └──────────┬──────────┘
                               │
                               ▼
                    ┌─────────────────────┐
                    │ Feature Extractor   │
                    └──────────┬──────────┘
                               │
             ┌─────────────────┼─────────────────┐
             │                 │                 │
             ▼                 ▼                 ▼
       Rule Engine       Learned Signals    Similarity
             │                 │                 │
             └─────────────────┼─────────────────┘
                               ▼
                    ┌─────────────────────┐
                    │ Classification      │
                    │ Engine              │
                    └──────────┬──────────┘
                               │
                     ┌─────────┴─────────┐
                     │                   │
              confidence >= T       confidence < T
                     │                   │
                     ▼                   ▼
                 Category         Question Engine
                                         │
                                         ▼
                                   User Answer
                                         │
                                         ▼
                                  Learning Engine
                                         │
                                         ▼
                                   Knowledge Base
```

---

# 4. Modelo conceptual

El sistema debe distinguir cuatro conceptos principales:

## Email

Representa el correo original y sus metadatos normalizados.

## Feature

Una característica observable del correo.

Ejemplos:

```text
sender.email
sender.domain
subject.tokens
body.language
body.contains_amount
body.contains_invoice
body.contains_transaction
headers.list_unsubscribe
attachment.count
attachment.types
thread.previous_category
```

## Category

Una clasificación definida por el usuario.

Ejemplo:

```text
Finance
├── Bank
├── Credit Card
├── Purchase
└── Taxes
```

Las categorías deben soportar jerarquía.

## Question

Una pregunta capaz de obtener información sobre una o más features.

Ejemplo:

```text
¿Este correo está relacionado con una transacción bancaria?
```

La pregunta puede mapear a:

```text
feature = transaction_type
answer:
    yes -> bank_transaction
    no  -> unknown
```

---

# 5. Modelo de datos

La implementación inicial debe utilizar SQLite.

## 5.1. emails

```sql
CREATE TABLE emails (
    id TEXT PRIMARY KEY,
    message_id TEXT,
    thread_id TEXT,
    sender_email TEXT,
    sender_name TEXT,
    sender_domain TEXT,
    subject TEXT,
    body_text TEXT,
    received_at DATETIME,
    raw_hash TEXT UNIQUE,
    created_at DATETIME NOT NULL
);
```

## 5.2. features

```sql
CREATE TABLE email_features (
    email_id TEXT NOT NULL,
    feature TEXT NOT NULL,
    value TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0,

    PRIMARY KEY (email_id, feature, value),
    FOREIGN KEY (email_id) REFERENCES emails(id)
);
```

## 5.3. categories

```sql
CREATE TABLE categories (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    name TEXT NOT NULL,
    description TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,

    FOREIGN KEY (parent_id) REFERENCES categories(id)
);
```

## 5.4. classifications

```sql
CREATE TABLE classifications (
    email_id TEXT NOT NULL,
    category_id TEXT NOT NULL,
    score REAL NOT NULL,
    method TEXT NOT NULL,
    explanation TEXT,
    created_at DATETIME NOT NULL,

    PRIMARY KEY (email_id, category_id),
    FOREIGN KEY (email_id) REFERENCES emails(id),
    FOREIGN KEY (category_id) REFERENCES categories(id)
);
```

`method` puede ser:

```text
rule
learned
similarity
model
user
combined
```

## 5.5. questions

```sql
CREATE TABLE questions (
    id TEXT PRIMARY KEY,
    text TEXT NOT NULL,
    feature TEXT NOT NULL,
    type TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1
);
```

Tipos iniciales:

```text
boolean
single_choice
multi_choice
text
category
```

## 5.6. question_answers

```sql
CREATE TABLE question_answers (
    id TEXT PRIMARY KEY,
    question_id TEXT NOT NULL,
    answer_value TEXT NOT NULL,
    feature TEXT NOT NULL,
    feature_value TEXT NOT NULL,
    FOREIGN KEY (question_id) REFERENCES questions(id)
);
```

## 5.7. interactions

Registrar cada interacción del usuario.

```sql
CREATE TABLE interactions (
    id TEXT PRIMARY KEY,
    email_id TEXT NOT NULL,
    question_id TEXT,
    answer TEXT,
    previous_prediction TEXT,
    resulting_category TEXT,
    created_at DATETIME NOT NULL,

    FOREIGN KEY (email_id) REFERENCES emails(id),
    FOREIGN KEY (question_id) REFERENCES questions(id)
);
```

## 5.8. learned_rules

```sql
CREATE TABLE learned_rules (
    id TEXT PRIMARY KEY,
    category_id TEXT NOT NULL,
    conditions TEXT NOT NULL,
    weight REAL NOT NULL,
    observations INTEGER NOT NULL DEFAULT 0,
    confirmations INTEGER NOT NULL DEFAULT 0,
    rejections INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,

    FOREIGN KEY (category_id) REFERENCES categories(id)
);
```

`conditions` debe almacenarse en un formato estructurado, preferiblemente JSON.

---

# 6. Representación de características

Las features deben utilizar nombres jerárquicos.

Ejemplos:

```text
sender.email
sender.domain
sender.is_known
sender.domain.is_corporate

content.language
content.intent
content.contains_invoice
content.contains_receipt
content.contains_transaction
content.contains_meeting
content.contains_newsletter

attachment.count
attachment.has_pdf
attachment.has_image
attachment.has_spreadsheet

headers.has_unsubscribe
headers.is_reply
headers.is_forward

thread.previous_category
thread.previous_sender
```

Las features deben tener:

```text
name
value
confidence
source
```

Ejemplo:

```json
{
  "name": "content.contains_invoice",
  "value": "true",
  "confidence": 0.97,
  "source": "pattern"
}
```

`source` puede ser:

```text
header
parser
pattern
classifier
llm
user
history
```

---

# 7. Extracción de features

La extracción debe funcionar en capas.

## 7.1. Headers

Extraer:

- From
- To
- Cc
- Reply-To
- Subject
- Date
- Message-ID
- In-Reply-To
- References
- List-Unsubscribe
- Precedence
- MIME information

## 7.2. Remitente

Extraer:

```text
email
domain
display name
domain frequency
known sender
known domain
previous categories
```

## 7.3. Contenido

Extraer:

```text
language
tokens
keywords
entities
URLs
monetary values
dates
invoice numbers
order numbers
transaction IDs
phone numbers
addresses
```

## 7.4. Intención

Detectar intenciones comunes:

```text
transaction
receipt
invoice
promotion
newsletter
notification
security_alert
meeting
personal_message
shipping
password_reset
account_update
social_notification
```

La lista debe ser extensible.

---

# 8. Motor de reglas

El motor de reglas es la primera línea de clasificación.

Ejemplo:

```text
IF sender.domain == "examplebank.com"
AND content.contains_transaction == true
THEN Finance/Bank
```

Las reglas deben soportar:

```text
equals
not_equals
contains
starts_with
ends_with
regex
exists
numeric comparison
set membership
```

Las reglas pueden combinar condiciones con:

```text
AND
OR
NOT
```

Cada regla debe producir un peso.

Ejemplo:

```text
bank-domain                 +0.70
transaction-language        +0.30
known-bank-sender           +0.90
```

El resultado se normaliza a un score entre `0` y `1`.

---

# 9. Clasificador probabilístico

El clasificador recibe:

```text
EmailFeatures
+
CategoryKnowledge
+
HistoricalEvidence
```

y produce:

```text
CategoryScore[]
```

Ejemplo:

```json
[
  {
    "category": "finance.bank",
    "score": 0.82
  },
  {
    "category": "finance.purchase",
    "score": 0.13
  },
  {
    "category": "work",
    "score": 0.05
  }
]
```

No se debe interpretar el score como una probabilidad estadística rigurosa a menos que el modelo haya sido calibrado.

---

# 10. Clasificación jerárquica

Las categorías deben clasificarse de arriba hacia abajo.

Ejemplo:

```text
Finance
 ├── Bank
 ├── Credit Card
 └── Purchase
```

Primero:

```text
Personal: 0.04
Work:     0.13
Finance:  0.83
```

Después, dentro de Finance:

```text
Bank:         0.70
Credit Card:  0.20
Purchase:     0.10
```

Resultado:

```text
Finance / Bank
```

Esto reduce el número de categorías que el motor de preguntas necesita considerar.

---

# 11. Motor Akinator

El motor Akinator debe mantener un conjunto de candidatos:

```text
Candidate {
    CategoryID
    Score
}
```

Ejemplo:

```text
Finance/Bank       0.41
Finance/Purchase   0.31
Work/Reports       0.18
Subscription       0.10
```

El motor selecciona preguntas para separar esos candidatos.

---

# 12. Selección de preguntas mediante información

Para una pregunta `Q`, calcular su utilidad.

Una implementación inicial puede utilizar entropía.

Entropía:

```text
H(C) = -Σ p(c) log2(p(c))
```

Para cada pregunta:

```text
InformationGain(Q) =
    H(before) - H(after)
```

La pregunta con mayor `InformationGain` debe tener prioridad.

Ejemplo:

```text
Pregunta A:
"¿Contiene un número de factura?"
Information gain = 0.31

Pregunta B:
"¿Conoces al remitente?"
Information gain = 0.08

Pregunta C:
"¿Es importante?"
Information gain = 0.02
```

Se selecciona A.

---

# 13. Consideraciones prácticas para las preguntas

No basta con maximizar información matemática.

Aplicar una penalización por:

- preguntas demasiado largas;
- preguntas ambiguas;
- preguntas cuya respuesta el sistema ya conoce;
- preguntas repetidas;
- preguntas que requieren demasiado contexto.

Una fórmula inicial:

```text
utility =
    information_gain
    * confidence_in_question
    * answer_reliability
    - repetition_penalty
    - complexity_penalty
```

---

# 14. Preguntas dinámicas

El sistema debe permitir preguntas predefinidas y preguntas generadas dinámicamente.

## Preguntas predefinidas

Ejemplo:

```text
¿Este correo está relacionado con una compra?
¿Es un correo de trabajo?
¿Está relacionado con una cuenta bancaria?
¿Es una promoción?
¿Esperabas este correo?
```

## Preguntas generadas

Cuando ninguna pregunta existente separa adecuadamente los candidatos, se puede solicitar a un LLM que genere una pregunta.

Entrada:

```json
{
  "candidate_categories": [
    "finance.bank",
    "finance.purchase"
  ],
  "known_features": {
    "contains_amount": true,
    "contains_transaction": true
  }
}
```

Salida esperada:

```json
{
  "question": "¿El correo informa sobre dinero que salió de tu cuenta bancaria?",
  "answers": [
    "yes",
    "no"
  ],
  "feature": "transaction.direction",
  "mapping": {
    "yes": "outgoing",
    "no": "other"
  }
}
```

Las preguntas generadas por un LLM deben validarse antes de incorporarse permanentemente al conocimiento.

---

# 15. Umbrales de decisión

Configurar al menos tres estados.

```text
HIGH_CONFIDENCE
MEDIUM_CONFIDENCE
LOW_CONFIDENCE
```

Ejemplo inicial:

```text
score >= 0.85
    → clasificar automáticamente

0.60 <= score < 0.85
    → considerar una pregunta

score < 0.60
    → preguntar o enviar a categoría Unknown
```

Estos valores deben ser configurables y posteriormente ajustarse con métricas reales.

---

# 16. Confirmación de clasificación

Cuando el sistema clasifica automáticamente, puede solicitar confirmación de forma selectiva.

Ejemplo:

```text
Clasifiqué este correo como:

Finance / Bank

¿Por qué?
- Dominio bancario conocido.
- Contiene una transacción.
- Coincide con 14 correos previamente clasificados.

[Correcto] [Cambiar]
```

Las confirmaciones no deben mostrarse para todos los mensajes.

Usar muestreo para obtener feedback sin molestar al usuario.

---

# 17. Aprendizaje

Cada interacción del usuario debe generar evidencia.

Ejemplo:

```text
Email:
sender = bank.example

Prediction:
Finance/Purchase = 0.52
Finance/Bank = 0.43

User:
Finance/Bank
```

El sistema registra:

```text
sender.domain = bank.example
    → Finance/Bank
```

con una determinada fuerza.

---

# 18. Aprendizaje incremental

Una regla aprendida debe ganar fuerza a medida que se confirma.

Ejemplo:

```text
1 confirmación  → weight 0.20
3 confirmaciones → weight 0.45
10 confirmaciones → weight 0.80
```

Una clasificación contradictoria debe reducir la fuerza:

```text
confirmations += 1
rejections += 1
```

Evitar que una sola clasificación errónea destruya conocimiento previamente aprendido.

---

# 19. Generalización

El sistema no debe memorizar solamente correos exactos.

Mala generalización:

```text
message-id XYZ → Bank
```

Buena generalización:

```text
sender.domain = bank.example
AND contains_transaction = true
→ Bank
```

Mejor aún:

```text
domain category:
    financial institution

intent:
    transaction

→ Finance/Bank
```

La generalización debe pasar progresivamente de:

```text
email
→ sender
→ domain
→ feature combination
→ semantic pattern
```

---

# 20. Correcciones del usuario

El usuario debe poder corregir:

```text
"Este correo debería estar en Work."
```

El sistema debe:

1. Cambiar la clasificación.
2. Registrar la corrección.
3. Detectar qué señales llevaron al error.
4. Ajustar sus pesos.
5. Evitar repetir el mismo error en correos similares.

---

# 21. Aprendizaje negativo

También es importante aprender:

```text
sender.domain = shop.example
≠ Finance/Bank
```

Una regla negativa puede ser tan útil como una positiva.

Ejemplo:

```text
shop.example
contains_transaction = true

NO → Finance/Bank
YES → Finance/Purchase
```

---

# 22. Similitud entre correos

Mantener opcionalmente una representación vectorial de los correos.

Puede utilizar:

- embeddings locales;
- embeddings mediante proveedor externo;
- TF-IDF inicialmente.

El sistema puede buscar correos similares ya clasificados.

Ejemplo:

```text
Nuevo correo
    ↓
Top 10 correos similares
    ↓
8 → Finance/Bank
2 → Finance/Purchase
    ↓
Evidence = Finance/Bank
```

Esta señal debe combinarse con reglas y features, no sustituirlas automáticamente.

---

# 23. LLM

El LLM debe ser opcional.

No debe ser requisito para ejecutar el sistema básico.

Usos recomendados:

```text
- extracción semántica
- clasificación difícil
- generación de preguntas
- explicación de decisiones
- normalización de categorías
- detección de intención
```

No usar el LLM para tareas que puedan resolverse de forma determinista:

```text
- parsing MIME
- extracción de headers
- hashing
- comparación exacta de dominios
- persistencia
- evaluación de reglas
```

---

# 24. Privacidad

El sistema debe diseñarse con enfoque local-first.

Prioridad:

```text
1. Procesamiento local
2. Modelo local
3. Proveedor externo únicamente si está habilitado
```

Nunca enviar automáticamente el contenido completo de los correos a un proveedor externo sin configuración explícita del usuario.

Separar:

```text
raw email
features
classification knowledge
```

para permitir eliminar el contenido del correo manteniendo, si el usuario lo desea, el conocimiento aprendido.

---

# 25. Arquitectura Go propuesta

Paquetes sugeridos:

```text
/internal
    /email
        parser.go
        normalize.go
        model.go

    /features
        extractor.go
        feature.go
        rules.go

    /categories
        category.go
        tree.go

    /classifier
        classifier.go
        scorer.go
        hierarchical.go

    /questions
        question.go
        selector.go
        information_gain.go

    /learning
        learner.go
        rules.go
        feedback.go

    /similarity
        similarity.go

    /llm
        provider.go
        extractor.go
        question_generator.go

    /storage
        database.go
        migrations.go

    /explain
        explanation.go
```

Interfaces importantes:

```go
type FeatureExtractor interface {
    Extract(ctx context.Context, email Email) ([]Feature, error)
}

type Classifier interface {
    Classify(
        ctx context.Context,
        email Email,
        features []Feature,
    ) (ClassificationResult, error)
}

type QuestionSelector interface {
    Select(
        ctx context.Context,
        candidates []Candidate,
        features []Feature,
        history []Interaction,
    ) (*Question, error)
}

type Learner interface {
    Learn(ctx context.Context, feedback Feedback) error
}
```

---

# 26. Pipeline principal

```go
func ProcessEmail(ctx context.Context, email Email) Result {
    normalized := normalizer.Normalize(email)

    features := extractor.Extract(ctx, normalized)

    rules := ruleEngine.Evaluate(features)

    classification := classifier.Classify(
        normalized,
        features,
        rules,
    )

    if classification.Confidence >= config.AutoClassifyThreshold {
        return Result{
            Category: classification.BestCategory,
            Confidence: classification.Confidence,
            NeedsQuestion: false,
        }
    }

    question := questionEngine.Select(
        classification.Candidates,
        features,
        history,
    )

    return Result{
        Classification: classification,
        Question: question,
        NeedsQuestion: question != nil,
    }
}
```

---

# 27. API

La API inicial puede utilizar REST.

## POST `/v1/emails/classify`

Entrada:

```json
{
  "id": "email-123",
  "sender": "billing@example.com",
  "subject": "Your monthly statement",
  "body": "Your statement is ready..."
}
```

Salida:

```json
{
  "classification": {
    "category": "finance.bank",
    "score": 0.91,
    "method": "combined"
  },
  "needs_question": false,
  "explanation": [
    "Known financial sender",
    "Statement detected",
    "Similar emails were classified as Bank"
  ]
}
```

---

## POST `/v1/emails/{id}/question`

Respuesta:

```json
{
  "question": {
    "id": "q-123",
    "text": "¿Este correo está relacionado con una transacción bancaria?",
    "type": "boolean",
    "answers": [
      {
        "id": "yes",
        "label": "Sí"
      },
      {
        "id": "no",
        "label": "No"
      }
    ]
  }
}
```

---

## POST `/v1/emails/{id}/answer`

Entrada:

```json
{
  "question_id": "q-123",
  "answer": "yes"
}
```

Salida:

```json
{
  "classification": {
    "category": "finance.bank",
    "score": 0.96
  },
  "learned": true
}
```

---

## POST `/v1/emails/{id}/feedback`

Entrada:

```json
{
  "correct": false,
  "category": "finance.purchase"
}
```

---

# 28. Configuración de categorías

El usuario debe poder definir categorías sin recompilar el programa.

Ejemplo YAML:

```yaml
categories:
  - id: work
    name: Work

  - id: personal
    name: Personal

  - id: finance
    name: Finance

  - id: finance.bank
    parent: finance
    name: Bank

  - id: finance.purchase
    parent: finance
    name: Purchase
```

---

# 29. Configuración de preguntas

Ejemplo:

```yaml
questions:
  - id: is_transaction
    text: "¿Este correo está relacionado con una transacción?"
    type: boolean
    feature: content.contains_transaction

  - id: is_work
    text: "¿Este correo está relacionado con tu trabajo?"
    type: boolean
    feature: context.is_work
```

---

# 30. Estado de una sesión de clasificación

Una sesión debe conservar:

```text
email
candidate categories
known features
asked questions
answers
classification scores
current confidence
```

Ejemplo:

```json
{
  "email_id": "123",
  "candidates": [
    {
      "category": "finance.bank",
      "score": 0.48
    },
    {
      "category": "finance.purchase",
      "score": 0.39
    }
  ],
  "asked_questions": [
    "is_transaction"
  ],
  "answers": {
    "is_transaction": "yes"
  }
}
```

---

# 31. Prevención de ciclos

El motor debe impedir:

```text
Pregunta A
→ Pregunta B
→ Pregunta A
```

También debe limitar:

```text
max_questions_per_email
```

Valor inicial recomendado:

```text
5
```

Si no se obtiene suficiente confianza después del límite:

```text
Unknown
```

o solicitar clasificación manual.

---

# 32. Categoría Unknown

Debe existir una categoría especial:

```text
unknown
```

No debe interpretarse como error.

Representa:

> El sistema no tiene suficiente evidencia para clasificar correctamente.

Esto evita clasificaciones arbitrarias.

---

# 33. Corrección manual

La interfaz debe permitir:

```text
Move to...
```

y seleccionar cualquier categoría.

La acción debe considerarse una señal de máxima confianza.

Ejemplo:

```text
user_feedback weight = 1.0
```

---

# 34. Métricas

Registrar como mínimo:

```text
classification_accuracy
auto_classification_rate
question_rate
correction_rate
average_questions_per_email
unknown_rate
precision_by_category
recall_by_category
```

También:

```text
question_information_gain
```

para determinar qué preguntas son realmente útiles.

---

# 35. Evaluación offline

Crear un dataset de evaluación:

```text
email
expected_category
```

Separar:

```text
training
validation
test
```

Nunca utilizar únicamente los correos de entrenamiento para medir precisión.

---

# 36. Evolución del sistema

## Fase 1 — MVP

Implementar:

- parser `.eml`;
- SQLite;
- categorías jerárquicas;
- extracción básica de features;
- reglas;
- clasificación por score;
- preguntas predefinidas;
- feedback del usuario;
- aprendizaje simple.

Sin LLM.

## Fase 2 — Adaptive Questions

Agregar:

- Information Gain;
- selección dinámica de preguntas;
- sesiones;
- explicación de decisiones;
- reglas aprendidas.

## Fase 3 — Similarity

Agregar:

- TF-IDF;
- búsqueda de correos similares;
- clasificación basada en ejemplos.

## Fase 4 — Semantic Intelligence

Agregar opcionalmente:

- embeddings;
- LLM;
- extracción semántica;
- generación de preguntas;
- explicación natural.

## Fase 5 — Production

Agregar:

- IMAP;
- Gmail;
- Microsoft Graph;
- procesamiento incremental;
- background workers;
- métricas;
- backups;
- controles de privacidad.

---

# 37. Requisitos funcionales

El sistema debe:

- [ ] aceptar correos normalizados;
- [ ] extraer features;
- [ ] soportar categorías jerárquicas;
- [ ] clasificar automáticamente;
- [ ] calcular confianza;
- [ ] explicar clasificaciones;
- [ ] detectar incertidumbre;
- [ ] seleccionar preguntas;
- [ ] registrar respuestas;
- [ ] aprender de respuestas;
- [ ] aprender de correcciones;
- [ ] soportar reglas negativas;
- [ ] evitar preguntas repetidas;
- [ ] soportar Unknown;
- [ ] mantener historial;
- [ ] funcionar sin LLM;
- [ ] permitir LLM como componente opcional.

---

# 38. Requisitos no funcionales

## Rendimiento

La clasificación de un correo que no requiere LLM debe ser suficientemente rápida para ejecutarse de forma interactiva.

Objetivo inicial:

```text
p95 < 100 ms
```

sin incluir proveedores externos.

## Determinismo

Dado:

```text
same email
+
same knowledge base
+
same configuration
```

el resultado debe ser determinista.

## Testabilidad

Cada componente debe poder probarse sin acceder a un proveedor de correo externo.

## Privacidad

El sistema debe funcionar localmente y minimizar la transmisión de contenido.

## Extensibilidad

Debe ser posible agregar:

```text
new feature
new classifier
new question type
new email provider
new LLM provider
```

sin modificar el núcleo.

---

# 39. Ejemplo completo

Email:

```text
From: alerts@mybank.example
Subject: Transaction completed

A transaction of $85.40 was completed on your account.
```

Features:

```text
sender.domain = mybank.example
sender.is_known = true
content.contains_amount = true
content.contains_transaction = true
content.intent = transaction
```

Rules:

```text
known_bank_domain       +0.70
transaction_detected    +0.20
financial_amount        +0.10
```

Resultado:

```text
Finance/Bank = 0.91
Finance/Purchase = 0.06
Other = 0.03
```

Acción:

```text
Auto-classify → Finance/Bank
```

Otro correo:

```text
From: store@example.com
Subject: Payment received
```

Resultado:

```text
Finance/Bank = 0.43
Finance/Purchase = 0.41
Work = 0.08
Other = 0.08
```

Pregunta seleccionada:

```text
¿Este correo corresponde a una compra realizada por ti?
```

Usuario:

```text
Sí
```

Resultado:

```text
Finance/Purchase = 0.94
```

Aprendizaje:

```text
store.example
+
payment_received
→ Finance/Purchase
```

---

# 40. Criterio de éxito

El sistema será considerado exitoso cuando pueda pasar de:

```text
Nuevo sistema
    ↓
Pregunta frecuentemente
    ↓
Aprende de las respuestas
    ↓
Pregunta menos
    ↓
Clasifica automáticamente la mayoría
    ↓
Pregunta solamente ante ambigüedad real
```

La métrica más importante no debe ser únicamente accuracy.

Debe medirse:

```text
correct classifications
──────────────────────────────
total user effort
```

El objetivo final es minimizar simultáneamente:

```text
errores
+
preguntas
+
configuración manual
```

manteniendo una clasificación suficientemente confiable.

---

# 41. Regla fundamental del diseño

El sistema no debe intentar "adivinar" silenciosamente cuando no tiene evidencia suficiente.

Debe existir una diferencia clara entre:

```text
"I know"
"I have a good prediction"
"I need to ask"
"I don't know"
```

Esa distinción es la característica central del sistema y la que permite que el modelo tipo Akinator sea útil para clasificación de correo.
