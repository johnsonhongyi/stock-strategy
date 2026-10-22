import { useCallback, useEffect, useState, type ReactNode, type FormEvent } from 'react';
import { Eye, EyeOff, Trash2, KeyRound, Shield, ShieldCheck, UserCheck, Plus, X, LogOut, Check } from 'lucide-react';

export type User = {
    id: number;
    username: string;
    admin: boolean;
};

export type Status = {
    authenticated: boolean;
    lan: boolean;
    user: User;
};

async function api<T>(path: string, body?: unknown, method?: 'GET' | 'POST' | 'PUT' | 'DELETE'): Promise<T> {
    const finalMethod = method || (body === undefined ? 'GET' : 'POST');
    const response = await fetch(`/api/auth/${path}`, {
        method: finalMethod,
        headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: 'same-origin',
    });
    const data = await response.json();
    if (!response.ok) {
        throw new Error(data.error || '请求失败');
    }
    return data;
}

function PasswordInput({
    value,
    onChange,
    placeholder,
    name,
    required = true,
    minLength,
    maxLength,
    autoComplete,
    ariaLabel,
}: {
    value?: string;
    onChange?: (val: string) => void;
    placeholder: string;
    name?: string;
    required?: boolean;
    minLength?: number;
    maxLength?: number;
    autoComplete?: string;
    ariaLabel?: string;
}) {
    const [visible, setVisible] = useState(false);
    return (
        <div style={{ position: 'relative', width: '100%', margin: '6px 0' }}>
            <input
                type={visible ? 'text' : 'password'}
                name={name}
                value={value}
                onChange={e => onChange?.(e.target.value)}
                placeholder={placeholder}
                required={required}
                minLength={minLength}
                maxLength={maxLength}
                autoComplete={autoComplete}
                aria-label={ariaLabel || placeholder}
                style={{
                    display: 'block',
                    width: '100%',
                    padding: '9px 36px 9px 12px',
                    color: '#0f172a',
                    background: '#ffffff',
                    border: '1px solid #cbd5e1',
                    borderRadius: 6,
                    fontSize: 14,
                    boxSizing: 'border-box',
                    outline: 'none',
                }}
            />
            <button
                type="button"
                onClick={() => setVisible(v => !v)}
                tabIndex={-1}
                title={visible ? '隐藏密码' : '显示密码'}
                aria-label={visible ? '隐藏密码' : '显示密码'}
                style={{
                    position: 'absolute',
                    right: 8,
                    top: '50%',
                    transform: 'translateY(-50%)',
                    background: 'none',
                    border: 'none',
                    cursor: 'pointer',
                    color: '#64748b',
                    padding: 4,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                }}
            >
                {visible ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
        </div>
    );
}

export function AuthGate({ children }: { children: ReactNode }) {
    const [status, setStatus] = useState<Status>();
    const [panel, setPanel] = useState(false);
    const [activeTab, setActiveTab] = useState<'users' | 'my-password'>('users');
    const [forceLogin, setForceLogin] = useState(false);
    const [users, setUsers] = useState<User[]>([]);
    const [error, setError] = useState('');
    const [message, setMessage] = useState('');
    const [busy, setBusy] = useState(false);

    // 添加新用户表单状态
    const [newUsername, setNewUsername] = useState('');
    const [newPassword, setNewPassword] = useState('');
    const [newPasswordConfirm, setNewPasswordConfirm] = useState('');
    const [newIsAdmin, setNewIsAdmin] = useState(false);

    // 针对某用户的快捷重置密码状态
    const [resetTargetUser, setResetTargetUser] = useState<string | null>(null);
    const [resetPasswordVal, setResetPasswordVal] = useState('');

    // 修改本人密码状态
    const [myOldPassword, setMyOldPassword] = useState('');
    const [myNewPassword, setMyNewPassword] = useState('');
    const [myNewPasswordConfirm, setMyNewPasswordConfirm] = useState('');

    const refresh = useCallback(async () => {
        try {
            const s = await api<Status>('status');
            setStatus(s);
            setError('');
        } catch (e) {
            setError(String(e));
        }
    }, []);

    const loadUsers = useCallback(async () => {
        try {
            const list = await api<User[]>('users');
            setUsers(list);
        } catch (e) {
            setError(String(e));
        }
    }, []);

    useEffect(() => {
        void refresh();
        const expired = () => {
            setStatus(undefined);
            void refresh();
        };
        window.addEventListener('stock-auth-required', expired);
        const timer = window.setInterval(() => void refresh(), 60000);
        return () => {
            window.removeEventListener('stock-auth-required', expired);
            clearInterval(timer);
        };
    }, [refresh]);

    useEffect(() => {
        if (panel && status?.user.admin) {
            void loadUsers();
        }
    }, [panel, status, loadUsers]);

    useEffect(() => {
        const openAccountManager = () => {
            if (status?.authenticated) {
                setPanel(true);
            } else {
                setForceLogin(true);
            }
        };
        window.addEventListener('stock-open-account-manager', openAccountManager);
        return () => window.removeEventListener('stock-open-account-manager', openAccountManager);
    }, [status?.authenticated]);

    // 登录提交
    async function handleLogin(e: FormEvent<HTMLFormElement>) {
        e.preventDefault();
        const form = e.currentTarget;
        const data = new FormData(form);
        const username = String(data.get('username') || '').trim();
        const password = String(data.get('password') || '');
        if (!username || !password) return;

        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api('login', { username, password });
            setForceLogin(false);
            await refresh();
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }

    // 添加新用户
    async function handleAddUser(e: FormEvent) {
        e.preventDefault();
        const u = newUsername.trim();
        if (!u) {
            setError('请输入用户名');
            return;
        }
        if (newPassword.length < 8) {
            setError('初始密码长度至少需 8 位');
            return;
        }
        if (newPassword !== newPasswordConfirm) {
            setError('两次输入的初始密码不一致，请核对');
            return;
        }

        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api('users', { username: u, password: newPassword, admin: newIsAdmin }, 'POST');
            setNewUsername('');
            setNewPassword('');
            setNewPasswordConfirm('');
            setNewIsAdmin(false);
            setMessage(`用户「${u}」添加成功！该用户已可使用新密码登录。`);
            await loadUsers();
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }

    // 修改用户角色
    async function handleToggleAdmin(target: User) {
        if (target.username.toLowerCase() === 'johnson' || target.id === 1) {
            setError('系统主管理员「johnson」的角色不可更改');
            return;
        }
        if (target.id === status?.user.id) {
            setError('不能取消当前登录管理员自身的管理员权限');
            return;
        }
        const nextAdmin = !target.admin;
        const actionDesc = nextAdmin ? '升级为管理员' : '降级为普通用户';
        if (!window.confirm(`确定要将用户「${target.username}」${actionDesc}吗？`)) {
            return;
        }

        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api('users', { id: target.id, username: target.username, admin: nextAdmin }, 'PUT');
            setMessage(`已将「${target.username}」${actionDesc}`);
            await loadUsers();
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }

    // 管理员重置指定用户密码
    async function handleResetUserPassword(username: string) {
        if (resetPasswordVal.length < 8) {
            setError('新密码长度至少需 8 位');
            return;
        }

        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api('reset-password', { username, password: resetPasswordVal }, 'POST');
            setResetTargetUser(null);
            setResetPasswordVal('');
            setMessage(`用户「${username}」密码重置成功！该用户已可使用新密码登录。`);
            await refresh();
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }

    // 删除用户
    async function handleDeleteUser(target: User) {
        if (target.username.toLowerCase() === 'johnson' || target.id === 1) {
            setError('系统主管理员「johnson」不可删除');
            return;
        }
        if (target.id === status?.user.id) {
            setError('不能删除当前登录账号');
            return;
        }
        if (!window.confirm(`确定要彻底删除用户「${target.username}」吗？\n删除后该用户将立即失效且不可恢复。`)) {
            return;
        }

        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api('users', { id: target.id, username: target.username }, 'DELETE');
            setMessage(`用户「${target.username}」已成功删除`);
            await loadUsers();
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }

    // 修改本人密码
    async function handleUpdateMyPassword(e: FormEvent) {
        e.preventDefault();
        if (!myOldPassword) {
            setError('请输入原密码');
            return;
        }
        if (myNewPassword.length < 8) {
            setError('新密码长度至少需 8 位');
            return;
        }
        if (myNewPassword !== myNewPasswordConfirm) {
            setError('两次输入的新密码不一致，请核对');
            return;
        }

        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api('password', { old_password: myOldPassword, password: myNewPassword });
            setMyOldPassword('');
            setMyNewPassword('');
            setMyNewPasswordConfirm('');
            setForceLogin(true);
            setPanel(false);
            await refresh();
            setMessage('密码已修改成功，请使用新密码重新登录');
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }

    // 退出登录
    async function handleLogout() {
        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api('logout', {});
            setPanel(false);
            await refresh();
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
        } finally {
            setBusy(false);
        }
    }

    const fieldStyle = {
        display: 'block',
        width: '100%',
        padding: '9px 12px',
        margin: '6px 0',
        color: '#0f172a',
        background: '#ffffff',
        border: '1px solid #cbd5e1',
        borderRadius: 6,
        fontSize: 14,
        boxSizing: 'border-box' as const,
        outline: 'none',
    };

    const notices = (
        <>
            {error && (
                <div role="alert" style={{ padding: '8px 12px', margin: '8px 0', background: '#fef2f2', border: '1px solid #fecaca', borderRadius: 6, color: '#dc2626', fontSize: 13, fontWeight: 500 }}>
                    {error}
                </div>
            )}
            {message && (
                <div role="status" style={{ padding: '8px 12px', margin: '8px 0', background: '#f0fdf4', border: '1px solid #bbf7d0', borderRadius: 6, color: '#16a34a', fontSize: 13, fontWeight: 500 }}>
                    {message}
                </div>
            )}
        </>
    );

    // 未登录视图（外网或内网强行登录）
    if (!status || (!status.authenticated && (!status.lan || forceLogin))) {
        return (
            <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', background: '#f1f5f9', padding: 16 }}>
                <section
                    style={{
                        background: '#ffffff',
                        color: '#0f172a',
                        padding: '28px 24px',
                        borderRadius: 12,
                        width: 380,
                        maxWidth: '95vw',
                        boxShadow: '0 10px 25px -5px rgba(0, 0, 0, 0.1), 0 8px 10px -6px rgba(0, 0, 0, 0.1)',
                        border: '1px solid #e2e8f0',
                    }}
                >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 18 }}>
                        <div style={{ background: '#2563eb', padding: 8, borderRadius: 8, color: '#fff', display: 'flex' }}>
                            <Shield size={22} />
                        </div>
                        <div>
                            <h1 style={{ fontSize: 20, fontWeight: 700, margin: 0, color: '#0f172a' }}>Easy Stock 身份验证</h1>
                            <p style={{ margin: '2px 0 0', fontSize: 12, color: '#64748b' }}>请登录以继续访问量化交易平台</p>
                        </div>
                    </div>

                    {notices}

                    {!status ? (
                        <button
                            type="button"
                            onClick={() => void refresh()}
                            style={{
                                width: '100%',
                                padding: '10px 0',
                                background: '#2563eb',
                                color: '#fff',
                                border: 'none',
                                borderRadius: 6,
                                fontWeight: 600,
                                cursor: 'pointer',
                            }}
                        >
                            重试连接服务
                        </button>
                    ) : (
                        <form onSubmit={handleLogin}>
                            <div style={{ marginBottom: 12 }}>
                                <label style={{ display: 'block', fontSize: 13, fontWeight: 600, color: '#334155', marginBottom: 4 }}>
                                    用户名
                                </label>
                                <input
                                    style={fieldStyle}
                                    name="username"
                                    aria-label="用户名"
                                    placeholder="请输入用户名（如 johnson / lili）"
                                    autoComplete="username"
                                    required
                                    maxLength={64}
                                />
                            </div>

                            <div style={{ marginBottom: 16 }}>
                                <label style={{ display: 'block', fontSize: 13, fontWeight: 600, color: '#334155', marginBottom: 4 }}>
                                    登录密码
                                </label>
                                <PasswordInput
                                    name="password"
                                    ariaLabel="密码"
                                    placeholder="请输入密码"
                                    autoComplete="current-password"
                                    required
                                    maxLength={256}
                                />
                            </div>

                            <button
                                type="submit"
                                disabled={busy}
                                style={{
                                    width: '100%',
                                    padding: '10px 0',
                                    background: '#2563eb',
                                    color: '#fff',
                                    border: 'none',
                                    borderRadius: 6,
                                    fontSize: 15,
                                    fontWeight: 600,
                                    cursor: busy ? 'not-allowed' : 'pointer',
                                    opacity: busy ? 0.7 : 1,
                                    boxShadow: '0 2px 4px rgba(37, 99, 235, 0.2)',
                                }}
                            >
                                {busy ? '正在登录...' : '立即登录'}
                            </button>

                            {status.lan && (
                                <button
                                    type="button"
                                    onClick={() => setForceLogin(false)}
                                    style={{
                                        width: '100%',
                                        marginTop: 10,
                                        padding: '9px 0',
                                        background: '#f8fafc',
                                        color: '#475569',
                                        border: '1px solid #cbd5e1',
                                        borderRadius: 6,
                                        fontSize: 14,
                                        cursor: 'pointer',
                                    }}
                                >
                                    直接返回内网免密页面
                                </button>
                            )}
                        </form>
                    )}
                </section>
            </div>
        );
    }

    // 已登录主体及账号管理抽屉/弹窗
    return (
        <>
            {children}
            {panel && (
                <div
                    style={{
                        position: 'fixed',
                        inset: 0,
                        background: 'rgba(15, 23, 42, 0.65)',
                        backdropFilter: 'blur(3px)',
                        display: 'grid',
                        placeItems: 'center',
                        zIndex: 2000,
                        padding: 16,
                    }}
                >
                    <section
                        role="dialog"
                        aria-modal="true"
                        aria-label="账号与用户管理"
                        style={{
                            background: '#ffffff',
                            color: '#0f172a',
                            padding: '24px 28px',
                            borderRadius: 14,
                            width: 600,
                            maxWidth: '96vw',
                            maxHeight: '92vh',
                            overflowY: 'auto',
                            boxShadow: '0 25px 50px -12px rgba(0, 0, 0, 0.25)',
                            border: '1px solid #e2e8f0',
                        }}
                    >
                        {/* 弹窗顶部栏 */}
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, borderBottom: '1px solid #f1f5f9', paddingBottom: 14 }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                                <div style={{ background: '#2563eb', padding: '6px 8px', borderRadius: 8, color: '#fff', display: 'flex' }}>
                                    <ShieldCheck size={20} />
                                </div>
                                <div>
                                    <h2 style={{ fontSize: 18, fontWeight: 700, margin: 0, color: '#0f172a' }}>账号与用户管理</h2>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 3 }}>
                                        <span style={{ fontSize: 13, color: '#64748b' }}>当前登录：</span>
                                        <strong style={{ fontSize: 13, color: '#0f172a' }}>{status.user.username}</strong>
                                        <span
                                            style={{
                                                fontSize: 11,
                                                padding: '1px 6px',
                                                borderRadius: 10,
                                                background: status.user.admin ? '#dbeafe' : '#f1f5f9',
                                                color: status.user.admin ? '#1d4ed8' : '#475569',
                                                fontWeight: 600,
                                            }}
                                        >
                                            {status.user.admin ? '系统管理员' : '普通用户'}
                                        </span>
                                    </div>
                                </div>
                            </div>
                            <button
                                type="button"
                                onClick={() => setPanel(false)}
                                title="关闭"
                                aria-label="关闭"
                                style={{
                                    background: '#f8fafc',
                                    border: '1px solid #cbd5e1',
                                    borderRadius: 6,
                                    padding: '6px 8px',
                                    cursor: 'pointer',
                                    display: 'flex',
                                    alignItems: 'center',
                                    color: '#64748b',
                                }}
                            >
                                <X size={18} />
                            </button>
                        </div>

                        {notices}

                        {/* 管理员 Tab 切换 */}
                        {status.user.admin && (
                            <div style={{ display: 'flex', gap: 8, marginBottom: 18, borderBottom: '1px solid #e2e8f0', paddingBottom: 8 }}>
                                <button
                                    type="button"
                                    onClick={() => setActiveTab('users')}
                                    style={{
                                        background: activeTab === 'users' ? '#2563eb' : '#f8fafc',
                                        color: activeTab === 'users' ? '#ffffff' : '#475569',
                                        border: '1px solid',
                                        borderColor: activeTab === 'users' ? '#2563eb' : '#cbd5e1',
                                        borderRadius: 6,
                                        padding: '7px 14px',
                                        fontSize: 13,
                                        fontWeight: 600,
                                        cursor: 'pointer',
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: 6,
                                    }}
                                >
                                    <UserCheck size={16} /> 用户管理（增 / 改 / 删）
                                </button>
                                <button
                                    type="button"
                                    onClick={() => setActiveTab('my-password')}
                                    style={{
                                        background: activeTab === 'my-password' ? '#2563eb' : '#f8fafc',
                                        color: activeTab === 'my-password' ? '#ffffff' : '#475569',
                                        border: '1px solid',
                                        borderColor: activeTab === 'my-password' ? '#2563eb' : '#cbd5e1',
                                        borderRadius: 6,
                                        padding: '7px 14px',
                                        fontSize: 13,
                                        fontWeight: 600,
                                        cursor: 'pointer',
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: 6,
                                    }}
                                >
                                    <KeyRound size={16} /> 修改我的密码
                                </button>
                            </div>
                        )}

                        {/* TAB 1: 管理员专属用户管理（用户列表 + 增改删 + 重置密码） */}
                        {status.user.admin && activeTab === 'users' && (
                            <div>
                                {/* 用户列表与操作 */}
                                <div style={{ marginBottom: 20 }}>
                                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
                                        <h3 style={{ fontSize: 15, fontWeight: 700, margin: 0, color: '#1e293b' }}>
                                            系统已注册用户（{users.length} 人）
                                        </h3>
                                        <span style={{ fontSize: 12, color: '#64748b' }}>
                                            支持角色调整、重置他人密码、删除用户
                                        </span>
                                    </div>

                                    <div style={{ border: '1px solid #e2e8f0', borderRadius: 8, overflow: 'hidden' }}>
                                        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
                                            <thead>
                                                <tr style={{ background: '#f8fafc', borderBottom: '1px solid #e2e8f0', color: '#475569' }}>
                                                    <th style={{ padding: '8px 12px', textAlign: 'left', fontWeight: 600 }}>用户名</th>
                                                    <th style={{ padding: '8px 12px', textAlign: 'left', fontWeight: 600 }}>权限角色</th>
                                                    <th style={{ padding: '8px 12px', textAlign: 'right', fontWeight: 600 }}>操作</th>
                                                </tr>
                                            </thead>
                                            <tbody>
                                                {users.map(u => {
                                                    const isJohnson = u.username.toLowerCase() === 'johnson' || u.id === 1;
                                                    const isSelf = u.id === status.user.id;
                                                    const isResetting = resetTargetUser === u.username;
                                                    return (
                                                        <tr key={u.id} style={{ borderBottom: '1px solid #f1f5f9', background: '#fff' }}>
                                                            <td style={{ padding: '10px 12px', fontWeight: 600, color: '#0f172a' }}>
                                                                {u.username}
                                                                {isJohnson && (
                                                                    <span style={{ marginLeft: 6, fontSize: 11, padding: '1px 5px', borderRadius: 4, background: '#fef3c7', color: '#92400e' }}>
                                                                        默认主管理
                                                                    </span>
                                                                )}
                                                                {isSelf && !isJohnson && (
                                                                    <span style={{ marginLeft: 6, fontSize: 11, padding: '1px 5px', borderRadius: 4, background: '#f1f5f9', color: '#475569' }}>
                                                                        当前登录
                                                                    </span>
                                                                )}
                                                            </td>
                                                            <td style={{ padding: '10px 12px' }}>
                                                                {isJohnson ? (
                                                                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color: '#1d4ed8', fontWeight: 600, fontSize: 12 }}>
                                                                        <Shield size={14} /> 管理员
                                                                    </span>
                                                                ) : (
                                                                    <button
                                                                        type="button"
                                                                        disabled={busy || isSelf}
                                                                        onClick={() => void handleToggleAdmin(u)}
                                                                        title={isSelf ? '不能修改自身角色' : '点击切换角色权限'}
                                                                        style={{
                                                                            display: 'inline-flex',
                                                                            alignItems: 'center',
                                                                            gap: 4,
                                                                            padding: '3px 8px',
                                                                            borderRadius: 6,
                                                                            border: '1px solid',
                                                                            borderColor: u.admin ? '#93c5fd' : '#cbd5e1',
                                                                            background: u.admin ? '#eff6ff' : '#f8fafc',
                                                                            color: u.admin ? '#1d4ed8' : '#64748b',
                                                                            fontSize: 12,
                                                                            fontWeight: 600,
                                                                            cursor: isSelf ? 'not-allowed' : 'pointer',
                                                                        }}
                                                                    >
                                                                        {u.admin ? <Shield size={13} /> : <UserCheck size={13} />}
                                                                        {u.admin ? '管理员' : '普通用户'}
                                                                        {!isSelf && <span style={{ fontSize: 10, color: '#94a3b8' }}>⇋</span>}
                                                                    </button>
                                                                )}
                                                            </td>
                                                            <td style={{ padding: '10px 12px', textAlign: 'right' }}>
                                                                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 6 }}>
                                                                    <button
                                                                        type="button"
                                                                        disabled={busy}
                                                                        onClick={() => {
                                                                            if (isResetting) {
                                                                                setResetTargetUser(null);
                                                                                setResetPasswordVal('');
                                                                            } else {
                                                                                setResetTargetUser(u.username);
                                                                                setResetPasswordVal('');
                                                                            }
                                                                        }}
                                                                        style={{
                                                                            padding: '4px 8px',
                                                                            background: isResetting ? '#e2e8f0' : '#f8fafc',
                                                                            color: '#334155',
                                                                            border: '1px solid #cbd5e1',
                                                                            borderRadius: 5,
                                                                            fontSize: 12,
                                                                            fontWeight: 500,
                                                                            cursor: 'pointer',
                                                                        }}
                                                                    >
                                                                        {isResetting ? '取消' : '重置密码'}
                                                                    </button>

                                                                    {!isJohnson && !isSelf && (
                                                                        <button
                                                                            type="button"
                                                                            disabled={busy}
                                                                            onClick={() => void handleDeleteUser(u)}
                                                                            title="删除此用户"
                                                                            style={{
                                                                                padding: '4px 8px',
                                                                                background: '#fef2f2',
                                                                                color: '#dc2626',
                                                                                border: '1px solid #fecaca',
                                                                                borderRadius: 5,
                                                                                fontSize: 12,
                                                                                fontWeight: 500,
                                                                                cursor: 'pointer',
                                                                                display: 'inline-flex',
                                                                                alignItems: 'center',
                                                                                gap: 3,
                                                                            }}
                                                                        >
                                                                            <Trash2 size={13} /> 删除
                                                                        </button>
                                                                    )}
                                                                </div>

                                                                {/* 行内重置密码面板 */}
                                                                {isResetting && (
                                                                    <div style={{ marginTop: 8, padding: 8, background: '#f8fafc', border: '1px solid #cbd5e1', borderRadius: 6, textAlign: 'left' }}>
                                                                        <div style={{ fontSize: 12, color: '#334155', fontWeight: 600, marginBottom: 4 }}>
                                                                            重置「{u.username}」的密码（至少8位）：
                                                                        </div>
                                                                        <PasswordInput
                                                                            value={resetPasswordVal}
                                                                            onChange={setResetPasswordVal}
                                                                            placeholder="请输入新密码（至少8位）"
                                                                            minLength={8}
                                                                        />
                                                                        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6, marginTop: 6 }}>
                                                                            <button
                                                                                type="button"
                                                                                onClick={() => setResetTargetUser(null)}
                                                                                style={{ padding: '4px 8px', background: '#fff', border: '1px solid #cbd5e1', borderRadius: 4, fontSize: 12 }}
                                                                            >
                                                                                取消
                                                                            </button>
                                                                            <button
                                                                                type="button"
                                                                                disabled={busy || resetPasswordVal.length < 8}
                                                                                onClick={() => void handleResetUserPassword(u.username)}
                                                                                style={{
                                                                                    padding: '4px 10px',
                                                                                    background: '#2563eb',
                                                                                    color: '#fff',
                                                                                    border: 'none',
                                                                                    borderRadius: 4,
                                                                                    fontSize: 12,
                                                                                    fontWeight: 600,
                                                                                    cursor: resetPasswordVal.length < 8 ? 'not-allowed' : 'pointer',
                                                                                }}
                                                                            >
                                                                                确认重置
                                                                            </button>
                                                                        </div>
                                                                    </div>
                                                                )}
                                                            </td>
                                                        </tr>
                                                    );
                                                })}
                                            </tbody>
                                        </table>
                                    </div>
                                </div>

                                {/* 添加新用户卡片 */}
                                <div style={{ background: '#f8fafc', border: '1px solid #e2e8f0', borderRadius: 8, padding: 14 }}>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 10 }}>
                                        <div style={{ background: '#10b981', color: '#fff', padding: 4, borderRadius: 4, display: 'flex' }}>
                                            <Plus size={14} />
                                        </div>
                                        <h3 style={{ fontSize: 14, fontWeight: 700, margin: 0, color: '#1e293b' }}>
                                            添加新用户
                                        </h3>
                                    </div>

                                    <form onSubmit={handleAddUser}>
                                        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                                            <div>
                                                <label style={{ display: 'block', fontSize: 12, fontWeight: 600, color: '#475569', marginBottom: 2 }}>
                                                    新用户名
                                                </label>
                                                <input
                                                    style={fieldStyle}
                                                    value={newUsername}
                                                    onChange={e => setNewUsername(e.target.value)}
                                                    placeholder="用户名（如 lili）"
                                                    required
                                                    maxLength={64}
                                                    autoComplete="off"
                                                />
                                            </div>
                                            <div style={{ display: 'flex', alignItems: 'center', marginTop: 18 }}>
                                                <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: '#334155', cursor: 'pointer' }}>
                                                    <input
                                                        type="checkbox"
                                                        checked={newIsAdmin}
                                                        onChange={e => setNewIsAdmin(e.target.checked)}
                                                    />
                                                    设为管理员权限
                                                </label>
                                            </div>
                                        </div>

                                        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10, marginTop: 4 }}>
                                            <div>
                                                <label style={{ display: 'block', fontSize: 12, fontWeight: 600, color: '#475569', marginBottom: 2 }}>
                                                    初始密码（至少 8 位）
                                                </label>
                                                <PasswordInput
                                                    value={newPassword}
                                                    onChange={setNewPassword}
                                                    placeholder="输入初始密码"
                                                    minLength={8}
                                                    required
                                                />
                                            </div>
                                            <div>
                                                <label style={{ display: 'block', fontSize: 12, fontWeight: 600, color: '#475569', marginBottom: 2 }}>
                                                    确认初始密码
                                                </label>
                                                <PasswordInput
                                                    value={newPasswordConfirm}
                                                    onChange={setNewPasswordConfirm}
                                                    placeholder="再次输入确认"
                                                    minLength={8}
                                                    required
                                                />
                                            </div>
                                        </div>

                                        <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 12 }}>
                                            <button
                                                type="submit"
                                                disabled={busy || !newUsername.trim() || newPassword.length < 8}
                                                style={{
                                                    padding: '8px 16px',
                                                    background: '#10b981',
                                                    color: '#fff',
                                                    border: 'none',
                                                    borderRadius: 6,
                                                    fontSize: 13,
                                                    fontWeight: 600,
                                                    cursor: busy || !newUsername.trim() || newPassword.length < 8 ? 'not-allowed' : 'pointer',
                                                    opacity: busy || !newUsername.trim() || newPassword.length < 8 ? 0.6 : 1,
                                                }}
                                            >
                                                确认添加用户
                                            </button>
                                        </div>
                                    </form>
                                </div>
                            </div>
                        )}

                        {/* TAB 2: 修改本人密码（所有人可用） */}
                        {(!status.user.admin || activeTab === 'my-password') && (
                            <div style={{ background: '#f8fafc', border: '1px solid #e2e8f0', borderRadius: 8, padding: 16 }}>
                                <h3 style={{ fontSize: 15, fontWeight: 700, margin: '0 0 12px', color: '#1e293b' }}>
                                    修改我的登录密码
                                </h3>

                                <form onSubmit={handleUpdateMyPassword}>
                                    <div style={{ marginBottom: 10 }}>
                                        <label style={{ display: 'block', fontSize: 12, fontWeight: 600, color: '#475569', marginBottom: 2 }}>
                                            当前旧密码
                                        </label>
                                        <PasswordInput
                                            value={myOldPassword}
                                            onChange={setMyOldPassword}
                                            placeholder="请输入当前正在使用的旧密码"
                                            required
                                        />
                                    </div>

                                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                                        <div>
                                            <label style={{ display: 'block', fontSize: 12, fontWeight: 600, color: '#475569', marginBottom: 2 }}>
                                                新密码（至少 8 位）
                                            </label>
                                            <PasswordInput
                                                value={myNewPassword}
                                                onChange={setMyNewPassword}
                                                placeholder="请输入新密码"
                                                minLength={8}
                                                required
                                            />
                                        </div>
                                        <div>
                                            <label style={{ display: 'block', fontSize: 12, fontWeight: 600, color: '#475569', marginBottom: 2 }}>
                                                确认新密码
                                            </label>
                                            <PasswordInput
                                                value={myNewPasswordConfirm}
                                                onChange={setMyNewPasswordConfirm}
                                                placeholder="再次输入新密码"
                                                minLength={8}
                                                required
                                            />
                                        </div>
                                    </div>

                                    <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 14 }}>
                                        <button
                                            type="submit"
                                            disabled={busy || !myOldPassword || myNewPassword.length < 8}
                                            style={{
                                                padding: '8px 16px',
                                                background: '#2563eb',
                                                color: '#fff',
                                                border: 'none',
                                                borderRadius: 6,
                                                fontSize: 13,
                                                fontWeight: 600,
                                                cursor: busy || !myOldPassword || myNewPassword.length < 8 ? 'not-allowed' : 'pointer',
                                            }}
                                        >
                                            确认修改并重新登录
                                        </button>
                                    </div>
                                </form>
                            </div>
                        )}

                        {/* 弹窗底部操作 */}
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 20, paddingTop: 14, borderTop: '1px solid #f1f5f9' }}>
                            <button
                                type="button"
                                disabled={busy}
                                onClick={handleLogout}
                                style={{
                                    display: 'inline-flex',
                                    alignItems: 'center',
                                    gap: 6,
                                    padding: '7px 14px',
                                    background: '#fef2f2',
                                    color: '#dc2626',
                                    border: '1px solid #fecaca',
                                    borderRadius: 6,
                                    fontSize: 13,
                                    fontWeight: 600,
                                    cursor: 'pointer',
                                }}
                            >
                                <LogOut size={15} /> 退出当前登录
                            </button>

                            <button
                                type="button"
                                onClick={() => setPanel(false)}
                                style={{
                                    padding: '7px 16px',
                                    background: '#f1f5f9',
                                    color: '#475569',
                                    border: '1px solid #cbd5e1',
                                    borderRadius: 6,
                                    fontSize: 13,
                                    fontWeight: 500,
                                    cursor: 'pointer',
                                }}
                            >
                                关闭
                            </button>
                        </div>
                    </section>
                </div>
            )}
        </>
    );
}
