# Basic Setup of MariaDB for Versipellis

1. [Start a MariaDB Docker container](https://hub.docker.com/_/mariadb)

   ```shell
   docker run --name my-mariadb -p 3306:3306 -e MARIADB_RANDOM_ROOT_PASSWORD=yes \
          -e MARIADB_DATABASE=versi_db -e MARIADB_USER=larry -e MARIADB_PASSWORD=awooo \
          --detach mariadb:latest
   ```

   (Alternative: [download and install MariaDB](https://mariadb.com/get-started-with-mariadb/))

2. [Connect to MariaDB](https://mariadb.com/docs/server/clients-and-utilities/mariadb-client/mariadb-command-line-client)

   ```shell
   docker exec -it my-mariadb mariadb --user=larry --password=awooo --database=versi_db
   ```

3. [Create a new table](https://mariadb.com/docs/server/server-usage/tables/create-table)[^1]

   ```sql
   CREATE TABLE IF NOT EXISTS input_data (
       id        INT         AUTO_INCREMENT PRIMARY KEY,
       name      VARCHAR(50) NOT NULL UNIQUE,
       last_seen TIMESTAMP   DEFAULT (NOW() - INTERVAL FLOOR(RAND() * 31536000) SECOND),
       rand_key  UUID        DEFAULT UUID_v4()
   );
   ```

   Optional check:

   ```sql
   DESCRIBE input_data;
   ```

4. [Insert rows into the table](https://mariadb.com/docs/server/reference/sql-statements/data-manipulation/inserting-loading-data/insert)

   ```sql
   INSERT INTO input_data (name) VALUES ('Alice'), ('Bob'), ('Carol'), ('Dave'), ('Eve');
   ```

   Optional check:

   ```sql
   SELECT * FROM input_data;
   ```

5. Exit

   ```
   \q
   ```

[^1]: The [`UUID_v4()`](https://mariadb.com/docs/server/reference/sql-functions/secondary-functions/miscellaneous-functions/uuid_v4) function is available since MariaDB 11.7.
