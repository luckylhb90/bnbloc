import { downloadCsv, req } from "./api.ts";
import { auditLogPath, type AuditLogFilter, type AuditLogReply, type AuditLogScope } from "./auditLog.ts";

const PAGE = 100;

// Audit log (R13) requests; the path rules live in auditLog.ts.
export const auditLogApi = {
  page: (scope: AuditLogScope, filter: AuditLogFilter, cursor?: string) =>
    req<AuditLogReply>(
      auditLogPath(scope, filter, { cursor, limit: PAGE, tzOffsetMin: new Date().getTimezoneOffset() }),
    ),
  // downloadCsv saves the whole filtered range; truncated reports the row cap
  // when the server cut the export short.
  downloadCsv: async (scope: AuditLogScope, filter: AuditLogFilter): Promise<{ truncated: number }> => {
    const path = auditLogPath(scope, filter, { csv: true, tzOffsetMin: new Date().getTimezoneOffset() });
    return downloadCsv(path, "audit-log.csv");
  },
};
