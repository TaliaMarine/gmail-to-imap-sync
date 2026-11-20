# Privacy policy

Gmail Archive Sync is software that you run yourself. This policy describes what that software does with your data.

**Who runs it.** The person who installs and operates the application on their own server is the only operator. The authors of the software do not operate any service, and they never receive your data, your credentials or any telemetry.

**What it accesses.** With your consent through Google's sign-in screen, the application gets read-only access to your Gmail account (the `gmail.readonly` scope). It reads your messages, their labels and your label names in order to copy the messages to an IMAP mail server that you configure. It cannot send, change or delete anything in Gmail.

**Where the data goes.** Message content is passed from Google to the IMAP server you chose, and nowhere else. The application does not store message bodies or attachments in its own database. It stores only: the account and destination settings you enter, synchronisation progress, and, for messages that failed to copy, their subject, sender, recipients, date and the error, so that you can retry them.

**Credentials.** Google refresh tokens, the Google client secret and IMAP passwords are stored encrypted on your own server and are never shown in the user interface or written to logs.

**Sharing.** No data is sold, shared with third parties, used for advertising, or used to train any model. Data flows only between Google, your server and your IMAP server.

**Retention and deletion.** Session history and logs are kept for the periods you configure. You can revoke the application's access at any time in your Google Account under Security → Third-party access, and delete all application data by deleting its database.

**Google API Services User Data Policy.** The application's use and transfer of information received from Google APIs adheres to the [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy), including the Limited Use requirements.

**Contact.** Questions about this policy: open an issue in this repository.
