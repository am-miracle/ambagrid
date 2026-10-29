SELECT 'CREATE DATABASE ambagrid_local_fleet OWNER ambagrid_admin'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'ambagrid_local_fleet')
\gexec
