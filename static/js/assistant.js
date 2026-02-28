// ForgeDesk AI Assistant — Alpine.js chat widget
function chatWidget() {
  return {
    open: false,
    currentConv: null,
    currentProjectID: null,
    messages: [],
    input: '',
    loading: false,
    streamingText: '',

    async toggle() {
      this.open = !this.open;
      if (this.open) {
        const pageProjectID = this.getProjectID();
        if (this.currentConv && this.currentProjectID !== pageProjectID) {
          this.currentConv = null;
          this.messages = [];
        }
        if (!this.currentConv) {
          await this.initConversation();
        }
        this.$nextTick(() => this.scrollToBottom());
        this.$nextTick(() => {
          const ta = this.$refs.chatInput;
          if (ta) ta.focus();
        });
      }
    },

    async initConversation() {
      const projectID = this.getProjectID();
      this.currentProjectID = projectID;
      const body = new URLSearchParams();
      if (projectID) body.set('project_id', projectID);

      try {
        const res = await fetch('/assistant/conversations', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: body.toString(),
        });
        if (!res.ok) return;
        const conv = await res.json();
        this.currentConv = conv.ID || conv.id;

        const msgRes = await fetch(`/assistant/conversations/${this.currentConv}/messages`);
        if (msgRes.ok) {
          const msgs = await msgRes.json();
          this.messages = msgs.map(m => ({
            role: m.Role || m.role,
            content: m.Content || m.content,
          }));
        }
      } catch (e) {
        console.error('Failed to init conversation:', e);
      }
    },

    renderMd(text, role) {
      if (!text) return '';
      // User messages: escape HTML only, preserve newlines
      if (role === 'user') {
        return text
          .replace(/&/g, '&amp;')
          .replace(/</g, '&lt;')
          .replace(/>/g, '&gt;')
          .replace(/\n/g, '<br>');
      }
      // Assistant messages: render markdown
      if (typeof marked !== 'undefined' && marked.parse) {
        return marked.parse(text, { breaks: true });
      }
      return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>');
    },

    async send() {
      const text = this.input.trim();
      if (!text || this.loading) return;

      this.input = '';
      this.messages.push({ role: 'user', content: text });
      this.loading = true;
      this.streamingText = '';
      this.$nextTick(() => this.scrollToBottom());

      try {
        const body = new URLSearchParams();
        body.set('content', text);

        const res = await fetch(`/assistant/conversations/${this.currentConv}/messages`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: body.toString(),
        });

        if (!res.ok) {
          this.messages.push({ role: 'assistant', content: 'Sorry, something went wrong.' });
          this.loading = false;
          return;
        }

        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        while (true) {
          const { done, value } = await reader.read();
          if (done) break;

          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split('\n');
          buffer = lines.pop();

          for (const line of lines) {
            if (!line.startsWith('data: ')) continue;
            const jsonStr = line.slice(6);
            try {
              const data = JSON.parse(jsonStr);
              if (data.error) {
                this.streamingText += data.error;
              } else if (data.done) {
                if (this.streamingText) {
                  this.messages.push({ role: 'assistant', content: this.streamingText });
                  this.streamingText = '';
                }
                if (data.reload) {
                  this.refreshPage();
                }
              } else if (data.text) {
                this.streamingText += data.text;
                this.$nextTick(() => this.scrollToBottom());
              }
            } catch (e) {
              // skip malformed JSON
            }
          }
        }

        if (this.streamingText) {
          this.messages.push({ role: 'assistant', content: this.streamingText });
          this.streamingText = '';
        }
      } catch (e) {
        console.error('Stream error:', e);
        if (this.streamingText) {
          this.messages.push({ role: 'assistant', content: this.streamingText });
          this.streamingText = '';
        } else {
          this.messages.push({ role: 'assistant', content: 'Connection lost. Please try again.' });
        }
      }

      this.loading = false;
      this.$nextTick(() => this.scrollToBottom());
    },

    refreshPage() {
      // Trigger a proper hx-boost navigation to refresh page content
      // while preserving the chat widget via hx-preserve
      setTimeout(() => {
        const a = document.createElement('a');
        a.href = window.location.pathname;
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        a.remove();
      }, 600);
    },

    async newConversation() {
      this.currentConv = null;
      this.currentProjectID = null;
      this.messages = [];
      this.streamingText = '';
      this.input = '';
      await this.initConversation();
    },

    handleKeydown(e) {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        this.send();
      }
    },

    scrollToBottom() {
      const el = this.$refs.chatMessages;
      if (el) el.scrollTop = el.scrollHeight;
    },

    getProjectID() {
      const el = document.getElementById('assistant-project-ctx');
      const id = el ? el.dataset.projectId : '';
      return id || null;
    },
  };
}
