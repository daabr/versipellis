# Versipellis Configuration File

## Pull Schedules

Pulling data usually requires a schedule configuration.

Versipellis supports the cronspec syntax `"MIN HRS DOM MON DOW"` (a string of 5 fields separated by spaces), as defined in the [OCPS 1.0 specification](https://github.com/open-source-cron/ocps/blob/main/specifications/OCPS-1.0.md).

| Field         | Values                  | Notes |
| ------------- | ----------------------- | ----- |
| Minutes       | `0`-`59`                |       |
| Hours         | `0`-`23`                |       |
| Days of Month | `1`-`31`                | 1     |
| Months        | `1`-`12` or `Jan`-`Dec` | 1, 2  |
| Days of Week  | `0`-`6` or `Sun`-`Sat`  | 2, 3  |

Notes:

1. `0` is not allowed
2. String values are case-insensitive
3. `7` is also allowed, it's equivalent to `0` and `Sun`

In addition, all the fields support specifying multiple values:

| Notation | Type     | Meaning                                                                    |
| -------- | -------- | -------------------------------------------------------------------------- |
| `*`      | Wildcard | All the allowed values of the field                                        |
| `A-B`    | Range    | All the values between `A` and `B`                                         |
| `A,B`... | List     | Either `A` or `B`, or `C`, etc.<br>(each may be a single value or a range) |

Wildcards (`*`) and ranges (`A-B`) also support an optional step suffix (`/N`) which filters their values to include only the lowest value, plus every `N`-th value after that.

Examples:

- `*/15` in the Minutes field is equivalent to `0,15,30,45`
- `1-5/2` in the Days-of-Week field is the same as `1,3,5` and `Mon,Wed,Fri`

If both the Days-of-Month and Days-of-Week fields are restricted (i.e. neither of them is `*`), a match occurs when **either** field matches the current time.

Example: The expression `"0 12 1 * MON"` will trigger at noon (`0 12`) on the first day of every month (`1`) **as well as** on every Monday (`MON`).

> [!TIP]
> Use <https://crontab.guru/> to check and experiment with cron expressions.

Versipellis also supports these predefined schedule aliases/nicknames, in accordance with [OCPS 1.1](https://github.com/open-source-cron/ocps/blob/main/increments/OCPS-increment-1.1.md):

| Alias                            | Equivalent To | Description                                   |
| -------------------------------- | ------------- | --------------------------------------------- |
| `@minutely`                      | `* * * * *`   | At the start of every minute                  |
| `@hourly`                        | `0 * * * *`   | At the start of every hour                    |
| `@daily` or `@midnight`          | `0 0 * * *`   | Every day at midnight                         |
| `@weekly`                        | `0 0 * * 0`   | Every Sunday at midnight                      |
| `@monthly`                       | `0 0 1 * *`   | On the first day of every month at midnight   |
| `@yearly` or `@annually`         | `0 0 1 1 *`   | Every year on January 1st at midnight         |
| `@every` [`Xh`][`Ym`][`Zs`]      | N/A           | Every `X` hours, `Y` minutes, and `Z` seconds |
| `@reboot` / `@startup` / `@once` | N/A           | Run once at startup.                          |

### Time Zones

The scheduler handles Daylight Saving Time (DST) transitions in the following way:

- "Spring forward": a scheduled time that falls into a DST gap (an hour that does not exist) is **skipped**
- "Fall back": a scheduled time that occurs twice, but with different time offsets (DST overlap) runs **twice**

In other words, the scheduler prioritizes a consistent cadence and accuracy of execution times over the number of executions per unit of time.

> [!TIP]
> It's recommended to use the default [UTC](https://en.wikipedia.org/wiki/UTC+00:00) instead of any timezone which is sensitive to DST transitions.
