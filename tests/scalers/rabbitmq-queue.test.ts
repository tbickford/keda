import * as async from 'async'
import * as fs from 'fs'
import * as sh from 'shelljs'
import * as tmp from 'tmp'
import * as amqp from 'amqplib/callback_api'
import test from 'ava'

const defaultNamespace = 'rabbitmq-queue-test'
const connectionString = 'amqp://guest:guest@192.168.1.158:5672/test-vm' // TODO: Replace this: process.env['TEST_STORAGE_CONNECTION_STRING']
const queueName = 'hello'

test.before(t => {
  if (!connectionString) {
    //  t.fail('TEST_STORAGE_CONNECTION_STRING environment variable is required for queue tests')
  }

  sh.config.silent = true
  const base64ConStr = Buffer.from(connectionString).toString('base64')
  const tmpFile = tmp.fileSync()
  fs.writeFileSync(tmpFile.name, deployYaml.replace('{{CONNECTION_STRING_BASE64}}', base64ConStr)
    .replace('{{CONNECTION_STRING}}', connectionString)
    .replace('{{QUEUE_NAME}}', queueName))
  sh.exec(`kubectl create namespace ${defaultNamespace}`)
  t.is(
    0,
    sh.exec(`kubectl apply -f ${tmpFile.name} --namespace ${defaultNamespace}`).code,
    'creating a deployment should work.'
  )
})

test.serial('Deployment should have 0 replicas on start', t => {
  const replicaCount = sh.exec(
    `kubectl get deployment.apps/test-deployment --namespace ${defaultNamespace} -o jsonpath="{.spec.replicas}"`
  ).stdout
  t.is(replicaCount, '0', 'replica count should start out as 0')
})

test.serial.cb(
  'Deployment should scale to 4 with 500 messages on the queue then back to 0',
  t => {

    // add 500 messages
    amqp.connect(connectionString,
      (err, connection) => {
        t.falsy(err, 'unable to create connection')
        connection.createConfirmChannel((err2, channel) => {
          t.falsy(err2, 'unable to create channel')
          channel.assertQueue(queueName, { durable: false })
          for (var n = 1; n <= 500; n++) {
            channel.sendToQueue(queueName, Buffer.from(`test ${n}`))
          }
          t.log('messages sense')

          channel.waitForConfirms((err) => {
            t.falsy(err, 'Waiting for channel errors')
            //sh.exec('sleep 30s')
            let replicaCount = '0'
            for (let i = 0; i < 10 && replicaCount !== '4'; i++) {
              replicaCount = sh.exec(
                `kubectl get deployment.apps/test-deployment --namespace ${defaultNamespace} -o jsonpath="{.spec.replicas}"`
              ).stdout
              t.log('replica count is:' + replicaCount)
              if (replicaCount !== '4') {
                sh.exec('sleep 5s')
              }
            }

            t.is('4', replicaCount, 'Replica count should be 4 after 10 seconds')

            for (let i = 0; i < 50 && replicaCount !== '0'; i++) {
              replicaCount = sh.exec(
                `kubectl get deployment.apps/test-deployment --namespace ${defaultNamespace} -o jsonpath="{.spec.replicas}"`
              ).stdout
              if (replicaCount !== '0') {
                sh.exec('sleep 5s')
              }
            }

            t.is('0', replicaCount, 'Replica count should be 0 after 3 minutes') 
            connection.close()
            t.end()
          })

        })
      })

  }
)


test.after.always.cb('clean up rabbitmq-queue deployment', t => {
  const resources = [
    'secret/test-secrets',
    'deployment.apps/test-deployment',
    'scaledobject.keda.k8s.io/test-scaledobject',
  ]

  for (const resource of resources) {
    sh.exec(`kubectl delete ${resource} --namespace ${defaultNamespace}`)
  }
  sh.exec(`kubectl delete namespace ${defaultNamespace}`)
  t.end()
  // delete test queue
  /*
  const queueSvc = rabbitmq.createQueueService(connectionString)
  queueSvc.deleteQueueIfExists('queue-name', err => {
    t.falsy(err, 'should delete test queue successfully')
    t.end()
  })
  */
})


const deployYaml = `apiVersion: v1
kind: Secret
metadata:
  name: test-secrets
data:
  RabbitMqHost: {{CONNECTION_STRING_BASE64}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deployment
  labels:
    app: test-deployment
spec:
  replicas: 0 
  selector:
    matchLabels:
      app: test-deployment
  template:
    metadata:
      labels:
        app: test-deployment
    spec:
      containers:
      - name: rabbitmq-consumer
        image: jeffhollan/rabbitmq-client:dev
        imagePullPolicy: Always
        command:
          - receive
        args:
          - '{{CONNECTION_STRING}}'
        envFrom:
        - secretRef:
            name: test-secrets
---
apiVersion: keda.k8s.io/v1alpha1
kind: ScaledObject
metadata:
  name: test-scaledobject
  labels:
    deploymentName: test-deployment
spec:
  scaleTargetRef:
    deploymentName: test-deployment
  pollingInterval: 5
  cooldownPeriod: 10
  minReplicaCount: 0
  maxReplicaCount: 4
  triggers:
  - type: rabbitmq
    metadata:
      queueName: {{QUEUE_NAME}}
      host: RabbitMqHost
      queueLength  : '50'`