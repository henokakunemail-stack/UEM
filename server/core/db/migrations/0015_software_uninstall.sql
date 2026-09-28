-- 0015_software_uninstall.sql
-- Silent uninstall support.
--
-- Extend the existing tables rather than adding a parallel pair. Every query the
-- console already runs -- the deployments list, the task drill-down, the
-- status rollup, the audit timeline -- keys on software_deployments.id or
-- deployment_tasks.deployment_id, so a new table would mean reimplementing all
-- of it and the operator would lose the ability to see an uninstall in the same
-- place they see an install.
--
-- Deliberately ALTER-only. Making package_id nullable would mean rebuilding both
-- tables, and SQLite cannot drop a foreign key in place, so a rebuild is
-- create-copy-drop-rename. With foreign_keys=ON (db.go:33) the DROP cascades
-- into deployment_tasks through the FK on software_deployments.id and destroys
-- every task row ever recorded. Turning the pragma off for the duration does
-- not help: PRAGMA foreign_keys is a no-op inside a transaction, and whether the
-- migration driver runs this script in one is not a property worth depending on.
--
-- The cost of staying ALTER-only is that an uninstall names a package this
-- platform knows about. That is the right boundary anyway: the operator is
-- removing a program from the approved baseline, which is a package in the
-- catalog, and it does not have to have been deployed from here for the
-- command to work.

-- Which operation this rollout performs. 'install' is the default and is
-- already true of every existing row, so this is a pure widening.
ALTER TABLE software_deployments ADD COLUMN action TEXT NOT NULL DEFAULT 'install';

CREATE INDEX IF NOT EXISTS idx_software_deployments_action ON software_deployments(action);

-- The argument list that actually ran, for the audit trail. A package's
-- uninstall_args is editable in the console after the fact, so what a given
-- task executed cannot be reconstructed from the package later.
ALTER TABLE deployment_tasks ADD COLUMN args TEXT NOT NULL DEFAULT '';
