// ForgeDesk AI Assistant — WebSocket-based shared project chat
function chatWidget() {
  return {
    open: false,
    enlarged: false,
    messages: [],
    input: '',
    loading: false,
    streamingText: '',
    unreadCount: 0,

    // WebSocket state
    _ws: null,
    _projectID: null,
    _convID: null,
    _reconnectAttempts: 0,
    _reconnectTimer: null,
    _currentUserID: null,
    _currentUserName: null,

    init() {
      const widget = this.$el;
      this._currentUserID = widget.dataset.userId || '';
      this._currentUserName = widget.dataset.userName || '';

      // Detect project page changes via htmx navigation
      document.addEventListener('htmx:afterSettle', () => {
        this._checkProjectChange();
      });
    },

    toggleEnlarge() {
      this.enlarged = !this.enlarged;
    },

    toggle() {
      this.open = !this.open;
      if (this.open) {
        this.unreadCount = 0;
        const pid = this._getProjectID();
        if (pid && pid !== this._projectID) {
          this._disconnect();
          this._connect(pid);
        } else if (pid && !this._ws) {
          this._connect(pid);
        }
        this.$nextTick(() => {
          this._scrollToBottom();
          const ta = this.$refs.chatInput;
          if (ta) ta.focus();
        });
      }
    },

    send() {
      const text = this.input.trim();
      if (!text || this.loading || !this._ws) return;

      this.input = '';
      // Do NOT add message locally — wait for server broadcast
      this._wsSend({ type: 'send_message', data: { content: text } });
    },

    handleKeydown(e) {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        this.send();
      }
    },

    renderMd(text, role) {
      if (!text) return '';
      if (role === 'user') {
        return text
          .replace(/&/g, '&amp;')
          .replace(/</g, '&lt;')
          .replace(/>/g, '&gt;')
          .replace(/\n/g, '<br>');
      }
      if (typeof marked !== 'undefined' && marked.parse) {
        return marked.parse(text, { breaks: true });
      }
      return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>');
    },

    refreshPage() {
      setTimeout(() => {
        const a = document.createElement('a');
        a.href = window.location.pathname;
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        a.remove();
      }, 600);
    },

    // ── WebSocket ──────────────────────────────

    _connect(projectID) {
      if (this._ws) this._disconnect();

      this._projectID = projectID;
      this.messages = [];
      this.streamingText = '';
      this._convID = null;

      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const url = proto + '//' + location.host + '/ws/assistant/' + projectID;

      try {
        this._ws = new WebSocket(url);
      } catch (e) {
        console.error('WebSocket connection failed:', e);
        return;
      }

      this._ws.onopen = () => {
        this._reconnectAttempts = 0;
      };

      this._ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data);
          this._handleWSMessage(msg);
        } catch (e) {
          console.error('Failed to parse WS message:', e);
        }
      };

      this._ws.onclose = () => {
        this._ws = null;
        this._tryReconnect();
      };

      this._ws.onerror = (err) => {
        console.error('WebSocket error:', err);
      };
    },

    _disconnect() {
      if (this._reconnectTimer) {
        clearTimeout(this._reconnectTimer);
        this._reconnectTimer = null;
      }
      this._reconnectAttempts = 0;
      if (this._ws) {
        this._ws.onclose = null; // prevent reconnect
        this._ws.close();
        this._ws = null;
      }
    },

    _tryReconnect() {
      if (this._reconnectAttempts >= 10) return;
      if (!this._projectID) return;

      const delay = Math.min(1000 * Math.pow(2, this._reconnectAttempts), 30000);
      this._reconnectAttempts++;

      this._reconnectTimer = setTimeout(() => {
        if (this._projectID) {
          this._connect(this._projectID);
        }
      }, delay);
    },

    _wsSend(data) {
      if (this._ws && this._ws.readyState === WebSocket.OPEN) {
        this._ws.send(JSON.stringify(data));
      }
    },

    _handleWSMessage(msg) {
      switch (msg.type) {
        case 'conv_info':
          this._convID = msg.data.conversation_id;
          break;

        case 'history':
          this.messages = (msg.data || []).map(m => ({
            id: m.id,
            role: m.role,
            content: m.content,
            user_id: m.user_id || null,
            user_name: m.user_name || '',
            created_at: m.created_at,
          }));
          this.$nextTick(() => this._scrollToBottom());
          break;

        case 'user_message':
          this.messages.push({
            id: msg.data.id,
            role: 'user',
            content: msg.data.content,
            user_id: msg.data.user_id,
            user_name: msg.data.user_name,
            created_at: msg.data.created_at,
          });
          // Set loading since AI will respond
          this.loading = true;
          this._incrementUnread();
          this.$nextTick(() => this._scrollToBottom());
          break;

        case 'ai_typing':
          this.loading = true;
          this.streamingText = '';
          break;

        case 'ai_chunk':
          if (msg.data && msg.data.text) {
            this.streamingText += msg.data.text;
            this.$nextTick(() => this._scrollToBottom());
          }
          break;

        case 'ai_done':
          if (this.streamingText) {
            this.messages.push({
              role: 'assistant',
              content: this.streamingText,
              user_name: '',
            });
            this.streamingText = '';
          }
          this.loading = false;
          this._incrementUnread();
          if (msg.data && msg.data.reload) {
            this.refreshPage();
          }
          this.$nextTick(() => this._scrollToBottom());
          break;

        case 'ai_error':
          this.loading = false;
          this.streamingText = '';
          const errText = (msg.data && msg.data.error) || 'Something went wrong';
          this.messages.push({
            role: 'assistant',
            content: 'Error: ' + errText,
            user_name: '',
          });
          this.$nextTick(() => this._scrollToBottom());
          break;
      }
    },

    _checkProjectChange() {
      const newPID = this._getProjectID();
      if (newPID !== this._projectID) {
        if (this._ws) this._disconnect();
        if (newPID && this.open) {
          this._connect(newPID);
        } else if (!newPID) {
          this._projectID = null;
          this.messages = [];
          this.streamingText = '';
        }
      }
    },

    _incrementUnread() {
      if (!this.open) {
        this.unreadCount++;
      }
    },

    _scrollToBottom() {
      const el = this.$refs.chatMessages;
      if (el) el.scrollTop = el.scrollHeight;
    },

    _getProjectID() {
      const el = document.getElementById('assistant-project-ctx');
      const id = el ? el.dataset.projectId : '';
      return id || null;
    },
  };
}
