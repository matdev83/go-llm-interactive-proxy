---
name: golang-services-adapters
description: Build and structure Go CLI applications (Cobra/Viper), gRPC services and interceptors, and database adapters with connection pooling and safe transactions.
---

# Go Services, CLI & Adapter Engineering Guide

This guide covers building production-ready infrastructure adapters: command-line tools with Cobra, high-performance gRPC services, and reliable database layers.

---

## 1. CLI Applications with `spf13/cobra`

### Structured Command Hierarchy & Context Propagation
- **Never call `os.Exit` inside commands**: Return errors from `RunE` so parent callers and test harnesses can capture and inspect errors.
- **Use `cmd.Context()`**: Always pass the command's context down to application use cases for cancellation and signal handling.
- **Respect Output Streams**: Output to `cmd.OutOrStdout()` and `cmd.ErrOrStderr()` instead of `fmt.Println` or `os.Stdout` to enable easy testing.

~~~go
func NewRootCmd() *cobra.Command {
    var configFile string

    cmd := &cobra.Command{
        Use:   "lipstd",
        Short: "LIP Standard Distribution Server",
        PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
            return initConfig(configFile)
        },
    }

    cmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "path to config file")
    cmd.AddCommand(newServeCmd())
    cmd.AddCommand(newVersionCmd())

    return cmd
}

func newServeCmd() *cobra.Command {
    var port int

    cmd := &cobra.Command{
        Use:   "serve",
        Short: "Start HTTP/gRPC proxy server",
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            server := app.NewServer(port)
            return server.Run(ctx)
        },
    }

    cmd.Flags().IntVarP(&port, "port", "p", 8080, "server listening port")
    return cmd
}
~~~

---

## 2. gRPC Services & Interceptors

### Unary Interceptors for Observability & Recovery
Compose middleware on gRPC servers using interceptors:

~~~go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

func RecoveryInterceptor() grpc.UnaryServerInterceptor {
    return func(
        ctx context.Context,
        req any,
        info *grpc.UnaryServerInfo,
        handler grpc.UnaryHandler,
    ) (resp any, err error) {
        defer func() {
            if r := recover(); r != nil {
                slog.ErrorContext(ctx, "panic in gRPC handler", "method", info.FullMethod, "panic", r)
                err = status.Errorf(codes.Internal, "internal server error")
            }
        }()
        return handler(ctx, req)
    }
}
~~~

### Graceful Shutdown
Always allow in-flight RPCs to finish during termination:
~~~go
grpcServer := grpc.NewServer(
    grpc.UnaryInterceptor(RecoveryInterceptor()),
)

// In shutdown handler:
stopped := make(chan struct{})
go func() {
    grpcServer.GracefulStop()
    close(stopped)
}()

select {
case <-stopped:
case <-time.After(10 * time.Second):
    grpcServer.Stop() // Force stop on timeout
}
~~~

---

## 3. Database Adapters & Connection Management

### Configuring `database/sql` Connection Pools
Never connect to SQL databases with default unbounded pool settings:

~~~go
func NewDB(dsn string) (*sql.DB, error) {
    db, err := sql.Open("pgx", dsn)
    if err != nil {
        return nil, err
    }

    // Crucial pool sizing configuration
    db.SetMaxOpenConns(25)                 // Match database capacity
    db.SetMaxIdleConns(25)                 // Keep idle connections warm
    db.SetConnMaxLifetime(5 * time.Minute) // Periodically refresh connections
    db.SetConnMaxIdleTime(1 * time.Minute)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := db.PingContext(ctx); err != nil {
        return nil, fmt.Errorf("ping database: %w", err)
    }

    return db, nil
}
~~~

### Safe Transaction Lifecycle Pattern
Ensure transactions are always safely rolled back on failure or panic:

~~~go
func (r *PostgresRepo) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
    tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    if err != nil {
        return fmt.Errorf("begin tx: %w", err)
    }

    defer func() {
        if p := recover(); p != nil {
            _ = tx.Rollback()
            panic(p) // re-throw panic after rollback
        } else if err != nil {
            _ = tx.Rollback()
        } else {
            err = tx.Commit()
        }
    }()

    return fn(tx)
}
~~~
