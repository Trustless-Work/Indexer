# Pluggable Sink Architecture — Research & Design

> Investigacion completa para abstraer la capa de salida del indexer, permitiendo enviar
> datos procesados a RabbitMQ, PostgreSQL, MongoDB, SQL Server, Firebase, o cualquier
> otro destino segun configuracion, sin modificar el nucleo del indexer.

---

## 1. Contexto y Punto de Integracion

### Estado Actual

El indexer procesa cada ledger en tres fases dentro de `internal/services/ingest.go`:

```go
func (m *ingestService) processLedger(ctx context.Context, ledgerMeta xdr.LedgerCloseMeta) error {
    // Phase 1: Get transactions from ledger
    transactions, err := m.getLedgerTransactions(ctx, ledgerMeta)

    // Phase 2: Process transactions using Indexer (parallel within ledger)
    buffer := indexer.NewIndexerBuffer()
    _, err = m.ledgerIndexer.ProcessLedgerTransactions(ctx, transactions, buffer)

    // Phase 3: Insert all data into DB  ← COMENTADO, SIN IMPLEMENTAR
    //if err := m.ingestProcessedData(ctx, buffer); err != nil {
    //    return fmt.Errorf("ingesting processed data for ledger %d: %w", ledgerSeq, err)
    //}

    return nil
}
```

**La Fase 3 es exactamente donde va el Sink.** El buffer esta lleno con toda la data
procesada pero actualmente se descarta.

### Que contiene el buffer por ledger

| Tipo | Struct | Descripcion |
|------|--------|-------------|
| `[]types.StateChange` | 11 categorias (BALANCE, ESCROW, ACCOUNT...) | Cambios de estado principal |
| `[]entities.Escrow` | Single/Multi release con milestones | Contratos de escrow detectados |
| `[]types.TrustlineChange` | Activos agregados/removidos | Cambios de trustline |
| `[]types.ContractChange` | Balances de tokens/contratos | Cambios de contratos SAC |
| `[]types.Transaction` | Con XDR completo | Transacciones del ledger |
| `[]types.Operation` | Con XDR completo | Operaciones individuales |

### Interface del buffer (existente)

```go
// internal/indexer/indexer.go
type IndexerBufferInterface interface {
    GetTransactions() []types.Transaction
    GetOperations() []types.Operation
    GetStateChanges() []types.StateChange
    GetTrustlineChanges() []types.TrustlineChange
    GetContractChanges() []types.ContractChange
    GetEscrows() []entities.Escrow
    GetAllParticipants() []string
    // ... mas metodos de push y merge
}
```

---

## 2. Patron Elegido: Interface Delgada + Registry

### Referencia de la industria

Este patron es el estandar de oro en Go. Proyectos de produccion que lo usan:

| Proyecto | Patron | Notas |
|----------|--------|-------|
| `database/sql` (stdlib) | Registry + Driver interface | Modelo canonico — `init()` + blank import |
| Telegraf (InfluxData) | Interface `Connect/Write/Close` + `init()` | 200+ output plugins |
| Watermill | `Publisher/Subscriber` interfaces | Kafka ↔ RabbitMQ ↔ Postgres con un cambio |
| Benthos / Redpanda Connect | `Output` interface + `public/service` API | Framework completo embebible |
| logrus | Hook interface | Cada hook es un sink |

### Por que NO una interface gruesa

Hacer una interface con metodos especificos de cada backend (confirmaciones de RabbitMQ,
transacciones de Postgres, write concern de Mongo) fuerza a todos los sinks a implementar
metodos que no tienen. El patron correcto es:

- **Interface minima obligatoria** — solo lo que todos comparten
- **Interfaces opcionales por capacidad** — chequeadas con type assertion

---

## 3. Diseño Detallado

### 3.1 Interface Principal

```go
// internal/sink/sink.go

package sink

import (
    "context"
    "github.com/Trustless-Work/Indexer/internal/indexer"
)

// Sink es la abstraccion de salida. Toda implementacion debe satisfacer esta interface.
// Las semanticas especificas de cada backend (confirms de RabbitMQ, transacciones de
// Postgres, write concern de Mongo) son detalles de implementacion — el caller ve
// solo error/nil.
type Sink interface {
    // Write recibe el buffer completo de un ledger ya procesado.
    // Debe ser seguro para llamadas concurrentes si se usa con MultiSink.
    Write(ctx context.Context, buffer indexer.IndexerBufferInterface, ledgerSeq uint32) error

    // Close libera recursos (conexiones, canales, pool de goroutines, etc.)
    // Write no debe ser llamado despues de Close.
    Close() error
}

// HealthChecker es opcional. Implementar si el sink soporta health/readiness probe.
type HealthChecker interface {
    Ping(ctx context.Context) error
}

// Flusher es opcional. Para sinks que acumulan un buffer interno y necesitan flush
// explicito (ej: batch publisher de RabbitMQ con ventana de tiempo).
type Flusher interface {
    Flush(ctx context.Context) error
}
```

### 3.2 Registry (modelo `database/sql`)

```go
// internal/sink/registry.go

package sink

import (
    "fmt"
    "sync"
)

// Factory crea un Sink a partir de configuracion generica.
// Cada implementacion decodifica los campos que necesita de cfg.
type Factory func(cfg map[string]any) (Sink, error)

var (
    mu       sync.RWMutex
    registry = make(map[string]Factory)
)

// Register registra una factory bajo un nombre.
// Se llama tipicamente desde init() en el paquete de cada implementacion.
// Panics si el nombre ya esta registrado (igual que database/sql).
func Register(name string, f Factory) {
    mu.Lock()
    defer mu.Unlock()
    if _, exists := registry[name]; exists {
        panic(fmt.Sprintf("sink: Register called twice for driver %q", name))
    }
    registry[name] = f
}

// Open crea un Sink por nombre usando la factory registrada.
func Open(name string, cfg map[string]any) (Sink, error) {
    mu.RLock()
    f, ok := registry[name]
    mu.RUnlock()
    if !ok {
        return nil, fmt.Errorf("sink %q not registered; did you import the package?", name)
    }
    return f(cfg)
}
```

### 3.3 MultiSink — Fan-out a multiples destinos

```go
// internal/sink/multi/multi.go

package multi

import (
    "context"
    "errors"
    "sync"

    "github.com/Trustless-Work/Indexer/internal/indexer"
    "github.com/Trustless-Work/Indexer/internal/sink"
)

// MultiSink implementa sink.Sink y escribe a todos los sinks en paralelo.
// Es transparente para el pipeline — el caller no sabe que hay multiples destinos.
type MultiSink struct {
    sinks []entry
}

type entry struct {
    sink     sink.Sink
    required bool // si required=false, un error se loguea pero no falla el Write
}

func New(sinks ...sink.Sink) *MultiSink {
    entries := make([]entry, len(sinks))
    for i, s := range sinks {
        entries[i] = entry{sink: s, required: true}
    }
    return &MultiSink{sinks: entries}
}

// WithOptional agrega un sink que no es requerido — su fallo no propaga error.
func (m *MultiSink) WithOptional(s sink.Sink) *MultiSink {
    m.sinks = append(m.sinks, entry{sink: s, required: false})
    return m
}

func (m *MultiSink) Write(ctx context.Context, buffer indexer.IndexerBufferInterface, ledgerSeq uint32) error {
    var (
        wg   sync.WaitGroup
        mu   sync.Mutex
        errs []error
    )
    for _, e := range m.sinks {
        wg.Add(1)
        go func(e entry) {
            defer wg.Done()
            if err := e.sink.Write(ctx, buffer, ledgerSeq); err != nil && e.required {
                mu.Lock()
                errs = append(errs, err)
                mu.Unlock()
            }
        }(e)
    }
    wg.Wait()
    return errors.Join(errs...)
}

func (m *MultiSink) Close() error {
    var errs []error
    for _, e := range m.sinks {
        errs = append(errs, e.sink.Close())
    }
    return errors.Join(errs...)
}
```

### 3.4 NoopSink — Para desarrollo y tests

```go
// internal/sink/noop/noop.go

package noop

import (
    "context"
    "github.com/Trustless-Work/Indexer/internal/sink"
    "github.com/Trustless-Work/Indexer/internal/indexer"
    "github.com/sirupsen/logrus"
)

func init() {
    sink.Register("noop", func(cfg map[string]any) (sink.Sink, error) {
        return &NoopSink{}, nil
    })
}

type NoopSink struct{}

func (n *NoopSink) Write(ctx context.Context, buffer indexer.IndexerBufferInterface, ledgerSeq uint32) error {
    logrus.WithField("ledger", ledgerSeq).Debug("noop sink: discarding buffer")
    return nil
}

func (n *NoopSink) Close() error { return nil }
```

---

## 4. Integracion en el Codigo Existente

### 4.1 Modificar `IngestServiceConfig`

```go
// internal/services/ingest.go

type IngestServiceConfig struct {
    // ... campos existentes sin cambios ...

    // Sink es la implementacion de salida inyectada.
    // Si es nil, se usa un NoopSink por defecto.
    Sink sink.Sink
}
```

### 4.2 Modificar `ingestService`

```go
type ingestService struct {
    // ... campos existentes sin cambios ...
    sink sink.Sink  // nuevo
}

func NewIngestService(cfg IngestServiceConfig) (*ingestService, error) {
    s := cfg.Sink
    if s == nil {
        s = &noop.NoopSink{} // fallback seguro
    }
    return &ingestService{
        // ... campos existentes ...
        sink: s,
    }, nil
}
```

### 4.3 Descomentar y adaptar Fase 3

```go
func (m *ingestService) processLedger(ctx context.Context, ledgerMeta xdr.LedgerCloseMeta) error {
    ledgerSeq := ledgerMeta.LedgerSequence()

    // Phase 1
    transactions, err := m.getLedgerTransactions(ctx, ledgerMeta)
    if err != nil {
        return fmt.Errorf("getting transactions for ledger %d: %w", ledgerSeq, err)
    }

    // Phase 2
    buffer := indexer.NewIndexerBuffer()
    _, err = m.ledgerIndexer.ProcessLedgerTransactions(ctx, transactions, buffer)
    if err != nil {
        return fmt.Errorf("processing transactions for ledger %d: %w", ledgerSeq, err)
    }

    // Phase 3: Enviar al sink configurado
    if err := m.sink.Write(ctx, buffer, ledgerSeq); err != nil {
        return fmt.Errorf("writing ledger %d to sink: %w", ledgerSeq, err)
    }

    return nil
}
```

### 4.4 Modificar `ingest.Config`

```go
// internal/ingest/ingest.go

type Config struct {
    // ... campos existentes sin cambios ...

    // SinkType determina que implementacion usar ("rabbitmq", "postgres", "mongodb", "noop")
    SinkType string

    // SinkOptions es configuracion especifica del sink elegido.
    // Cada implementacion decodifica los campos que necesita.
    SinkOptions map[string]any
}
```

### 4.5 Modificar `setupDeps()`

```go
// internal/ingest/ingest.go

func setupDeps(cfg Config) (*services.IngestService, error) {
    // ... setup existente ...

    // Construir sink segun configuracion
    s, err := sink.Open(cfg.SinkType, cfg.SinkOptions)
    if err != nil {
        return nil, fmt.Errorf("creating sink %q: %w", cfg.SinkType, err)
    }

    return services.NewIngestService(services.IngestServiceConfig{
        // ... campos existentes ...
        Sink: s,
    })
}
```

### 4.6 `cmd/ingest.go` — blank imports

```go
package main

import (
    // Importar los sinks que se quieran activar en este binario
    _ "github.com/Trustless-Work/Indexer/internal/sink/rabbitmq"
    _ "github.com/Trustless-Work/Indexer/internal/sink/postgres"
    // _ "github.com/Trustless-Work/Indexer/internal/sink/mongodb"
    // _ "github.com/Trustless-Work/Indexer/internal/sink/noop"

    "github.com/Trustless-Work/Indexer/internal/ingest"
)

func main() {
    cfg := ingest.Config{
        // ... config existente ...
        SinkType: "rabbitmq",
        SinkOptions: map[string]any{
            "url":      "amqp://guest:guest@localhost:5672/",
            "exchange": "stellar.events",
        },
    }
    ingest.Ingest(cfg)
}
```

---

## 5. Implementaciones Planificadas

### 5.1 RabbitMQ Sink

**Libreria:** `github.com/rabbitmq/amqp091-go` v1.10.x (oficial RabbitMQ team)
> `streadway/amqp` esta deprecated — su propio README apunta al nuevo.

**Patron de publicacion:** Batch por ledger + publisher confirms async

```go
// internal/sink/rabbitmq/rabbitmq.go

func init() {
    sink.Register("rabbitmq", func(cfg map[string]any) (sink.Sink, error) {
        return New(cfg)
    })
}

type Config struct {
    URL              string `mapstructure:"url"`
    Exchange         string `mapstructure:"exchange"`
    PublisherConfirms bool  `mapstructure:"publisher_confirms"`
}

type RabbitMQSink struct {
    conn     *amqp091.Connection
    ch       *amqp091.Channel
    confirms <-chan amqp091.Confirmation
    cfg      Config
}
```

**Exchange design — Topic Exchange:**
```
stellar.<network>.<entity>.<action>

stellar.testnet.escrow.created
stellar.testnet.state_change.balance
stellar.testnet.payment.sent
stellar.testnet.trustline.added
```

**Mensaje envelope:**
```go
type MessageEnvelope struct {
    MessageID string          `json:"message_id"`
    Timestamp time.Time       `json:"timestamp"`   // ledger time
    Version   string          `json:"version"`     // "1.0"
    EventType string          `json:"event_type"`
    Network   string          `json:"network"`
    LedgerSeq uint32          `json:"ledger_seq"`
    Payload   json.RawMessage `json:"payload"`
}
```

**Propiedades AMQP obligatorias:**
- `DeliveryMode: amqp091.Persistent` (=2) — sobrevive restart del broker
- `ContentType: "application/json"`
- `Timestamp`: del ledger, no `time.Now()`

**Reconexion:** Loop con exponential backoff (1s → 2s → ... → 60s cap), watch `conn.NotifyClose()`.

**Dead Letter Queue:**
```
Queue: escrow.processor
  x-dead-letter-exchange:     stellar.dlx
  x-dead-letter-routing-key:  escrow.processor

Queue: escrow.processor.dlq  (destino final de mensajes fallidos)
```

### 5.2 PostgreSQL Sink

**Libreria:** `github.com/jackc/pgx/v5` — driver mas rapido, soporta COPY protocol

**Patron de insert:** `pgx.CopyFrom` (COPY protocol) — 10-50x mas rapido que INSERT para batches

```go
func (s *PostgresSink) Write(ctx context.Context, buffer indexer.IndexerBufferInterface, ledgerSeq uint32) error {
    // Construir rows desde el buffer
    rows := make([][]interface{}, 0)
    for _, sc := range buffer.GetStateChanges() {
        rows = append(rows, []interface{}{sc.LedgerNumber, sc.StateChangeCategory, ...})
    }

    // COPY protocol — el mas rapido para bulk insert
    _, err := s.pool.CopyFrom(
        ctx,
        pgx.Identifier{"state_changes"},
        []string{"ledger_number", "category", "reason", ...},
        pgx.CopyFromRows(rows),
    )
    return err
}
```

### 5.3 MongoDB Sink

**Libreria:** `go.mongodb.org/mongo-driver/v2`

**Patron:** `collection.BulkWrite()` con `InsertOne` models

```go
func (s *MongoSink) Write(ctx context.Context, buffer indexer.IndexerBufferInterface, ledgerSeq uint32) error {
    models := make([]mongo.WriteModel, 0)
    for _, escrow := range buffer.GetEscrows() {
        models = append(models, mongo.NewInsertOneModel().SetDocument(escrow))
    }
    _, err := s.collection.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
    return err
}
```

---

## 6. Estructura de Archivos Final

```
internal/
└── sink/
    ├── sink.go              # Interface Sink + HealthChecker + Flusher
    ├── registry.go          # Register() / Open()
    ├── multi/
    │   └── multi.go         # MultiSink — fan-out en paralelo
    ├── noop/
    │   └── noop.go          # Descarta todo — para dev/tests
    ├── rabbitmq/
    │   ├── rabbitmq.go      # Implementacion + init()
    │   ├── config.go        # RabbitMQConfig struct
    │   └── reconnect.go     # Loop de reconexion
    ├── postgres/
    │   ├── postgres.go      # Implementacion + init()
    │   └── config.go        # PostgresConfig struct
    └── mongodb/
        ├── mongodb.go       # Implementacion + init()
        └── config.go        # MongoConfig struct
```

**Archivos modificados del codigo existente (minimo):**

| Archivo | Cambio |
|---------|--------|
| `internal/services/ingest.go` | +campo `sink` en struct; descomentar fase 3 |
| `internal/ingest/ingest.go` | +campos `SinkType`/`SinkOptions` en Config; construir sink en `setupDeps()` |
| `cmd/ingest.go` | +blank imports + config del sink |

El nucleo del indexer (indexer.go, processors/, entities/, buffer) **no se toca**.

---

## 7. Docker Compose (desarrollo local)

```yaml
services:
  rabbitmq:
    image: rabbitmq:3.13-management-alpine
    hostname: rabbitmq          # critico para persistencia de datos
    ports:
      - "5672:5672"             # AMQP
      - "15672:15672"           # Management UI: http://localhost:15672
    volumes:
      - rabbitmq_data:/var/lib/rabbitmq/mnesia
    healthcheck:
      test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: indexer
      POSTGRES_PASSWORD: indexer
      POSTGRES_DB: indexer
    volumes:
      - pg_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U indexer"]
      interval: 5s
      retries: 5

  stellar-indexer:
    build: .
    depends_on:
      rabbitmq:
        condition: service_healthy
      postgres:
        condition: service_healthy
    environment:
      SINK_TYPE: rabbitmq
      RABBITMQ_URL: amqp://guest:guest@rabbitmq:5672/

volumes:
  rabbitmq_data:
  pg_data:
```

> **Nota:** Usar `hostname: rabbitmq` en el servicio de RabbitMQ es obligatorio.
> RabbitMQ usa el hostname para nombrar sus archivos de base de datos (Mnesia).
> Sin esto, el volumen no se reutiliza entre reinicios.

---

## 8. Patron del API Consumer (servicio separado)

Si se usa RabbitMQ como sink, el API consumer sigue este patron:

```
RabbitMQ Queue
      │
      ▼  autoAck: false (SIEMPRE manual)
Consumer goroutine
      │  acumula batch (100 msgs o 2 segundos)
      ▼
pgx.CopyFrom → PostgreSQL   ← insert masivo via COPY protocol
      │
      ├─ Exito → msg.Ack(multiple=true)
      └─ Error → msg.Nack(requeue=true) → DLQ tras N reintentos
```

**Librerias para el API:**
- Consumer: `github.com/rabbitmq/amqp091-go` v1.10.x
- DB: `github.com/jackc/pgx/v5` v5.7.x
- UUID: `github.com/google/uuid` v1.6.x

---

## 9. Veredicto de Viabilidad

| Aspecto | Resultado |
|---------|-----------|
| Viabilidad tecnica | **100% viable** |
| Invasion al codigo existente | **Minima** — 3 archivos modificados, nucleo intacto |
| Extensibilidad | **Total** — nuevo sink = 1 archivo + 1 blank import |
| Fan-out simultaneo | **Nativo** via `MultiSink` |
| Semanticas distintas por backend | **Encapsuladas** — el caller ve solo `error/nil` |
| Patron de referencia | `database/sql`, Telegraf, Watermill, Benthos |
| Sinks planificados | RabbitMQ, PostgreSQL, MongoDB (mas cualquier futuro) |

---

## 10. Dependencias a Agregar

```
go get github.com/rabbitmq/amqp091-go@latest      # RabbitMQ (oficial)
go get github.com/jackc/pgx/v5@latest              # PostgreSQL
go get go.mongodb.org/mongo-driver/v2@latest       # MongoDB (opcional)
go get github.com/google/uuid@latest               # Message IDs
```

---

*Documento generado: 2026-03-17*
*Estado: Investigacion completa — pendiente implementacion*
