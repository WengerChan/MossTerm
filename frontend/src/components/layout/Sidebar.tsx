/**
 * Sidebar
 * --------------------------------------------------------------------
 * 左侧侧栏：
 *   - 顶部：搜索框 + 新建 profile 按钮
 *   - 中部：Profile 树（SessionTree）
 *   - 底部：快速入口（命令面板 / SFTP / 设置）
 */
import { Search, Plus, Terminal, FolderOpen, Settings } from "lucide-react";
import { useSessionStore } from "@components/session/sessionStore";
import { useUIStore } from "@stores/uiStore";
import { SessionTree } from "@components/session/SessionTree";
import { Button } from "@components/common/Button";

export interface SidebarProps {
  className?: string;
}

export function Sidebar({ className }: SidebarProps): JSX.Element {
  const keyword     = useSessionStore((s) => s.form.searchKeyword);
  const setKeyword  = useSessionStore((s) => s.setSearchKeyword);
  const startCreate = useSessionStore((s) => s.startCreate);
  const openPalette = useUIStore((s) => s.openCommandPalette);
  const openSftp    = useUIStore((s) => s.toggleSftpPanel);
  const sftpOpen    = useUIStore((s) => s.sftpPanelVisible);

  return (
    <div className={`flex h-full flex-col ${className ?? ""}`}>
      {/* 顶部：搜索 + 新建 */}
      <div className="flex items-center gap-1.5 border-b border-moss-border p-2">
        <div className="relative flex-1">
          <Search
            size={12}
            className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-ink-muted"
          />
          <input
            type="text"
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="搜索 profile…"
            className="moss-input pl-6 text-xs"
          />
        </div>
        <Button
          variant="primary"
          size="sm"
          icon={<Plus size={12} />}
          onClick={() => startCreate()}
          title="新建 profile"
        />
      </div>

      {/* 中部：profile 树 */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <SessionTree />
      </div>

      {/* 底部：快捷入口 */}
      <div className="flex items-center gap-1 border-t border-moss-border p-2">
        <Button
          size="sm"
          icon={<Terminal size={12} />}
          onClick={openPalette}
          title="命令面板 (Ctrl+Shift+P)"
        >
          命令
        </Button>
        <Button
          size="sm"
          icon={<FolderOpen size={12} />}
          onClick={openSftp}
          active={sftpOpen}
          title="SFTP 面板 (Ctrl+Alt+S)"
        >
          SFTP
        </Button>
        <div className="flex-1" />
        <Button
          size="sm"
          icon={<Settings size={12} />}
          title="设置 (Ctrl+,)"
          onClick={() => {
            // TODO: 打开设置
          }}
        />
      </div>
    </div>
  );
}
