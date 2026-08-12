# HTTP Archive as a corpus

HTTP Archive runs Wappalyzer across tens of millions of pages every month and publishes the
results as a public BigQuery dataset. The `httparchive` seed adapter reads a CSV you export
from it rather than querying BigQuery itself.

## Why an export instead of a client

Querying BigQuery from Go means the `cloud.google.com/go` tree, gRPC, and OAuth — several
hundred transitive packages in a tool whose `go.mod` is otherwise empty. Reading a CSV keeps
that cost at zero, keeps the query in your own project on your own billing, and leaves the
query editable: the recipe below is a starting point, not a contract.

## What the GraphQL label actually means

Read this before trusting a result set.

HTTP Archive's `GraphQL` technology is detected two ways, and neither works well:

- Its `xhr` rule is `graphql\.[\w]+\.(?:com|net)/`. That pattern is matched against a bare
  hostname, which never has the trailing `/` the pattern requires, so **the rule effectively
  never fires**.
- Its `meta` rule looks for a `store-config` meta tag containing `graphqlMethod`, a
  Shopify-ecosystem artifact.

Almost everything labelled `GraphQL` therefore arrives through `implies` from another
technology. Ten carry it: Apollo, commercetools, Front-Commerce, GraphCMS, Ikas, MyWebsite Now,
Nacelle, RedwoodJS, Saleor, and Sitecore Experience Edge.

Measured on the `2026-07-01` partition, mobile root pages:

| | pages |
| --- | --- |
| labelled `GraphQL` | 56,495 |
| of which also Apollo | 43,234 |
| of which also MyWebsite Now | 8,172 |
| of which also Ikas | 4,483 |
| **direct `meta` or `xhr` hit, no implying technology** | **412** |

So the label means "Apollo Client, or one of about nine GraphQL-backed commerce and CMS
platforms" — roughly 0.35% of pages, far below real GraphQL adoption. It is a precise signal
for a narrow population, not a survey. For recall, query `httparchive.crawl.requests` for
`/graphql` in `url` instead; that scans about 0.30 TB per partition.

## The query

`httparchive.crawl.pages` requires a partition filter on `date`, so a query without one is
rejected rather than expensive. Partitions are always dated the first of the month and land
one to six weeks late, so do not hardcode a date. The `httparchive.latest.pages` view resolves
the newest partition for you at the same cost:

```sql
SELECT DISTINCT root_page AS page, rank
FROM `httparchive.latest.pages`
WHERE client = 'mobile'
  AND is_root_page
  AND EXISTS (
    SELECT 1 FROM UNNEST(technologies) AS t
    WHERE t.technology = 'GraphQL'
  )
ORDER BY rank
```

Keep it a single statement. The equivalent with a `DECLARE` that reads
`INFORMATION_SCHEMA.PARTITIONS` works and costs the same, but it makes the job a script, and
`bq` echoes every statement of a script above its results — so the CSV gains a preamble that
is not data. The adapter skips such a preamble, but the view avoids producing one.

Three details matter for cost:

- **Use the `EXISTS` form.** The `'GraphQL' IN UNNEST(technologies.technology)` shorthand that
  HTTP Archive's own documentation uses defeats leaf-field pruning and scans 30.9 GB instead of
  13.4 GB for the same answer.
- **Filter the cluster keys in order** — `client`, then `is_root_page`. Both together took a
  measured query from a 13.4 GB dry-run estimate to 3.83 GB actually billed.
- **Never `SELECT *`.** One partition is 34.6 TB, about 2,600 times the targeted query.

`LIMIT` does not reduce billed bytes.

## Running it

```sh
bq query --use_legacy_sql=false --format=csv --maximum_bytes_billed=20000000000 \
  < graphql-sites.sql > graphql-sites.csv

./gqlcrawl seeds --source httparchive --input graphql-sites.csv --limit 500
```

Always set `--maximum_bytes_billed` so a mistake fails instead of billing. The query above was
measured at 3.96 GB billed against a 13.2 GB dry-run estimate — clustering prunes what a dry
run cannot predict, so budget from the estimate and expect to pay about a third of it. At US
multi-region on-demand pricing that is roughly two and a half cents, and the first TiB per
billing account each month is free. The dataset is readable by any authenticated
Google identity; you need a project only because the job is billed to one. The BigQuery
sandbox works without a credit card.

The job must run in the `US` multi-region — the dataset lives there and a job pinned
elsewhere will not resolve the table.

## What the adapter accepts

Any CSV whose header names a value column: `page`, `root_page`, `origin`, `site`, `url`,
`domain`, or `host`. A `rank` column is read as a CrUX magnitude bucket, which is what HTTP
Archive stores — the value is a bucket ceiling and order within a bucket is arbitrary. A file
with no recognizable header is read from its first column.

By default seeds are emitted as hosts, which deduplicates an export that has several pages per
site. Pass `--option kind=url` to keep the exported URLs, and `--option label=VALUE` to stamp a
crawl date or query name into each seed's evidence field.
