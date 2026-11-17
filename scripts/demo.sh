#!/bin/sh
set -eu
base="${POCKETSEARCH_URL:-http://127.0.0.1:8080}"
curl --fail-with-body -sS -X PUT "$base/documents/go-backend" -H 'Content-Type: application/json' \
  -d '{"title":"Go backend developer","text":"Удалённая работа: PostgreSQL, поиск, распределённые системы","category":"jobs"}'
curl --fail-with-body -sS -X PUT "$base/documents/cpp-systems" -H 'Content-Type: application/json' \
  -d '{"title":"C++ systems engineer","text":"Телеметрия, SQLite, конкурентность. Возможен переход на Go","category":"jobs"}'
curl --fail-with-body -sS -X PUT "$base/documents/go-course" -H 'Content-Type: application/json' \
  -d '{"title":"Практический Go","text":"HTTP, тестирование, базы данных","category":"courses"}'
curl --fail-with-body -sS --get "$base/search" --data-urlencode 'q=go' --data-urlencode 'category=jobs'
