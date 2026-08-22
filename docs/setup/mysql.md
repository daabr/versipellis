# Basic Setup of MySQL for Versipellis

1. [Start a MySQL Docker container](https://hub.docker.com/_/mysql)

   ```shell
   docker run --name my-mysql -p 3306:3306 -e MYSQL_RANDOM_ROOT_PASSWORD=yes \
          -e MYSQL_DATABASE=versi_db -e MYSQL_USER=larry -e MYSQL_PASSWORD=awooo \
          --detach mysql:latest
   ```

   (Alternative: [download and install MySQL](https://dev.mysql.com/doc/mysql-getting-started/en/))

2. [Connect to MySQL](https://dev.mysql.com/doc/refman/en/mysql.html)

   ```shell
   docker exec -it my-mysql mysql --user=larry --password=awooo --database=versi_db
   ```

3. [Create a new table](https://dev.mysql.com/doc/refman/en/create-table.html) [^1]

   ```sql
   CREATE TABLE IF NOT EXISTS input_data (
       id        INT         AUTO_INCREMENT PRIMARY KEY,
       name      VARCHAR(50) NOT NULL UNIQUE,
       last_seen TIMESTAMP   DEFAULT (NOW() - INTERVAL FLOOR(RAND() * 31536000) SECOND),
       rand_key  BINARY(16)  DEFAULT (UUID_TO_BIN(UUID(), 1))
   );
   ```

   Optional check:

   ```sql
   DESCRIBE input_data;
   ```

4. [Insert rows into the table](https://dev.mysql.com/doc/refman/en/insert.html)

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

[^1]: See the reference documentation for the [`UUID_TO_BIN()`](https://dev.mysql.com/doc/refman/en/miscellaneous-functions.html#function_uuid-to-bin) function for the meaning of `swap_flag=1`
