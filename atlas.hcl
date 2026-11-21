env "local" {
  src = "file://database/schema.sql"
  dev = "docker://postgres/19/dev?search_path=public"
  url = getenv("APP_DATABASE_URL")
  migration {
    dir = "file://database/migrations"
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}
